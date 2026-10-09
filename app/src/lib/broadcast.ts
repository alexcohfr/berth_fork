import { create } from "zustand";

import { toastManager } from "@/components/ui/toast";
import { errorMessage } from "@/lib/format";
import { plainError } from "@/lib/errors";
import { Cancelled, isWaitingRefusal } from "@/lib/orchestrate-core";
import { send, waitSent } from "@/lib/orchestrate";
import { openComposer, useComposer } from "@/lib/composer";
import { boxOffline, enqueue, sendFailure } from "@/lib/queue";
import { boxHasRuns, runs as runsApi, scheduleRuns } from "@/lib/runs";
import { terminal, walkSteps } from "@/lib/orchestrate-core";
import { boxApi, type Run } from "@/lib/api";
import { meaningfulTail } from "@/lib/screen";
import { useStore } from "@/lib/store";

// A broadcast sends one prompt to many agents: one after another, so a box
// that fails or is slow never leaves the rest half sent, then (if asked)
// waits for every turn to end and keeps the last thing each agent said.

// offline: its box is away and the prompt was not sent; deferred: handed to
// the agent's offline queue, to be typed in once the box is back.
export type RowState = "queued" | "sending" | "sent" | "working" | "finished" | "waiting" | "timed-out" | "exited" | "failed" | "stopped" | "offline" | "deferred";

export interface RunRow {
  box: string;
  session: string;
  text: string;
  files?: { uri: string; name?: string }[];
  idem_key?: string;
  state: RowState;
  // The box's broadcast run this row is part of, on a box with runs.
  run?: string;
  error?: string;
  // The last lines on the agent's screen once its turn ended.
  tail?: string[];
}

export interface BroadcastRun {
  id: string;
  title: string;
  wait: boolean;
  rows: RunRow[];
  done: boolean;
}

interface RunState {
  run?: BroadcastRun;
  controller?: AbortController;
}

export const useBroadcastRun = create<RunState>()(() => ({}));

export const ENDED: RowState[] = ["finished", "waiting", "timed-out", "exited", "failed", "stopped", "offline", "deferred"];

const patch = (id: string, i: number, p: Partial<RunRow>) =>
  useBroadcastRun.setState((s) => (s.run?.id === id ? { run: { ...s.run, rows: s.run.rows.map((r, j) => (j === i ? { ...r, ...p } : r)) } } : s));

async function tailOf(box: string, session: string): Promise<string[] | undefined> {
  const c = useStore.getState().client;
  if (!c) return undefined;
  try {
    const { screen } = await c.box<{ screen: string }>(box, "GET", `sessions/${encodeURIComponent(session)}/screen`);
    return meaningfulTail(screen ?? "", 8);
  } catch {
    return undefined;
  }
}

export function summarize(rows: RunRow[]): string {
  const n = (s: RowState) => rows.filter((r) => r.state === s).length;
  const parts = [
    n("finished") && `${n("finished")} finished`,
    n("waiting") && `${n("waiting")} need${n("waiting") === 1 ? "s" : ""} you`,
    n("sent") && `${n("sent")} sent`,
    n("working") && `${n("working")} working`,
    n("timed-out") && `${n("timed-out")} still going`,
    n("exited") && `${n("exited")} exited`,
    n("failed") && `${n("failed")} failed`,
    n("deferred") && `${n("deferred")} queued for when ${n("deferred") === 1 ? "its box is" : "their boxes are"} back`,
    n("offline") && `${n("offline")} on offline ${n("offline") === 1 ? "box" : "boxes"}`,
    n("stopped") && `${n("stopped")} not sent`,
  ];
  return parts.filter(Boolean).join(" · ");
}

// startBroadcast sends each item in turn. Waiting happens alongside: the
// next send does not wait for the previous agent's turn.
// queueRow hands one row whose box is away to the offline queue.
export async function queueRow(runId: string, i: number) {
  const row = useBroadcastRun.getState().run?.rows[i];
  if (!row || useBroadcastRun.getState().run?.id !== runId) return;
  try {
    await enqueue({ id: row.idem_key, box: row.box, session: row.session, text: row.text, toast: false, native: row.files ? { idem_key: row.idem_key!, when: "idle", files: row.files } : undefined });
    patch(runId, i, { state: "deferred", error: undefined });
  } catch (err) {
    patch(runId, i, { state: "failed", error: plainError(err) });
  }
}

// startBroadcast sends each item in turn; with queueOffline, prompts for
// agents whose box is away go to the offline queue instead of failing.
export function startBroadcast(o: { title: string; wait: boolean; timeout?: number; queueOffline?: boolean; items: { box: string; session: string; text: string; files?: { uri: string; name?: string }[] }[] }) {
  useBroadcastRun.getState().controller?.abort();
  const controller = new AbortController();
  const { signal } = controller;
  const id = Math.random().toString(36).slice(2);
  const rows: RunRow[] = o.items.map((it) => ({ ...it, idem_key: it.files ? crypto.randomUUID() : undefined, state: "queued" }));
  useBroadcastRun.setState({ run: { id, title: o.title, wait: o.wait, rows, done: false }, controller });

  void (async () => {
    const waits: Promise<void>[] = [];
    // On boxes with runs, each box's share is one broadcast run there: the
    // box holds each prompt until its agent is idle, waits for every turn,
    // and keeps going if the app quits. Other boxes are sent to from here.
    const onBox = new Map<string, number[]>();
    for (const [i, it] of o.items.entries()) {
      if (!it.files && boxHasRuns(it.box) && !boxOffline(it.box)) onBox.set(it.box, [...(onBox.get(it.box) ?? []), i]);
    }
    for (const [box, rows] of onBox) waits.push(broadcastRun(id, box, rows, o, signal));
    for (const [i, it] of o.items.entries()) {
      if (onBox.get(it.box)?.includes(i)) continue;
      if (signal.aborted) {
        patch(id, i, { state: "stopped" });
        continue;
      }
      const away = () => (o.queueOffline ? queueRow(id, i) : patch(id, i, { state: "offline", error: `${it.box} is offline` }));
      if (boxOffline(it.box)) {
        await away();
        continue;
      }
      patch(id, i, { state: "sending" });
      let sent: Awaited<ReturnType<typeof send>>;
      try {
        // when "now": the box refuses to type into an agent at a question,
        // whose answer is the person's to give.
        const client = useStore.getState().client;
        if (it.files && !client) throw new Error("Shipyard disconnected");
        sent = it.files ? await boxApi.send(client!, it.box, it.session, it.text, true, { when: "idle", idem_key: rows[i].idem_key, files: it.files }) : await send(it.box, it.session, it.text, { when: "now" });
      } catch (err) {
        if (isWaitingRefusal(err)) {
          patch(id, i, { state: "waiting", error: "Not sent: it is waiting for you" });
          continue;
        }
        const f = sendFailure(err, it.box);
        if (f?.kind === "offline") await away();
        else patch(id, i, { state: "failed", error: f?.message ?? errorMessage(err) });
        continue;
      }
      if (!o.wait) {
        patch(id, i, { state: "sent" });
        continue;
      }
      patch(id, i, { state: "working" });
      waits.push(
        waitSent(it.box, it.session, sent, { timeout: o.timeout ?? 1800, signal })
          .then(async (res) => {
            const state: RowState = res.timed_out ? "timed-out" : res.state === "exited" || res.state === "lost" ? "exited" : res.state === "waiting" ? "waiting" : "finished";
            patch(id, i, { state, tail: await tailOf(it.box, it.session) });
          })
          .catch((err) => {
            // Stopping leaves a sent prompt sent; it only stops watching.
            if (err instanceof Cancelled || signal.aborted) patch(id, i, { state: "sent" });
            else patch(id, i, { state: "failed", error: plainError(err) });
          }),
      );
    }
    await Promise.all(waits);
    const s = useBroadcastRun.getState();
    if (s.run?.id !== id) return;
    useBroadcastRun.setState({ run: { ...s.run, done: true }, controller: undefined });
    // Out of sight, say how it went.
    if (!useComposer.getState().draft?.results) {
      toastManager.add({
        title: `“${o.title}” ${o.wait ? "is done" : "was sent"}`,
        description: summarize(useBroadcastRun.getState().run?.rows ?? []),
        type: "success",
        actionProps: { children: "Results", onClick: () => openComposer({ results: true }) },
      });
    }
  })();
}

// broadcastRun starts one box's broadcast run and keeps its rows in step.
async function broadcastRun(id: string, box: string, rows: number[], o: { title: string; wait: boolean; timeout?: number; items: { box: string; session: string; text: string }[] }, signal: AbortSignal) {
  for (const i of rows) patch(id, i, { state: "sending" });
  let runId: string;
  try {
    const run = await runsApi.start(box, {
      template: "broadcast",
      title: o.title,
      group: id,
      params: { sessions: rows.map((i) => ({ session: o.items[i].session, text: o.items[i].text })), wait: o.wait, timeout: `${o.timeout ?? 1800}s` },
    });
    runId = run.id;
    scheduleRuns(box, 0);
  } catch (err) {
    for (const i of rows) patch(id, i, { state: "failed", error: plainError(err) });
    return;
  }
  for (const i of rows) patch(id, i, { run: runId });
  const apply = (r: Run) => {
    const map = r.steps.find((s) => s.kind === "map");
    rows.forEach((i, k) => {
      const item = map?.children?.find((c) => c.path === `${map.path}.i${k}`);
      let send: string | undefined, sendErr: string | undefined, turn: string | undefined, turnOut: string | undefined, turnErr: string | undefined;
      walkSteps(item?.children, (s) => {
        if (s.kind === "prompt") [send, sendErr] = [s.status, s.error];
        if (s.kind === "wait") [turn, turnOut, turnErr] = [s.status, s.output, s.error];
      });
      let state: RowState = "sending";
      if (send === "failed") state = /waiting for you/.test(sendErr ?? "") ? "waiting" : "failed";
      else if (send === "succeeded") state = o.wait ? "working" : "sent";
      if (turn === "succeeded") state = turnOut === "waiting" ? "waiting" : "finished";
      else if (turn === "failed") state = /did not finish/.test(turnErr ?? "") ? "timed-out" : /exited/.test(turnErr ?? "") ? "exited" : "failed";
      else if (turn === "cancelled" || (terminal(r.status) && r.status === "cancelled" && state === "working")) state = "stopped";
      const error = state === "failed" ? (sendErr ?? turnErr ?? r.error) : state === "waiting" && send === "failed" ? "Not sent: it is waiting for you" : undefined;
      patch(id, i, { state, error });
    });
  };
  // Stopping the broadcast cancels its runs; the prompts already typed stay.
  const onAbort = () => void runsApi.cancel(box, runId).catch(() => {});
  signal.addEventListener("abort", onAbort, { once: true });
  for (;;) {
    let r: Run;
    try {
      r = await runsApi.get(box, runId);
    } catch {
      await new Promise((res) => setTimeout(res, 3000));
      continue;
    }
    apply(r);
    if (terminal(r.status)) break;
    await new Promise((res) => setTimeout(res, 1500));
  }
  signal.removeEventListener("abort", onAbort);
  if (o.wait) await Promise.all(rows.map(async (i) => patch(id, i, { tail: await tailOf(box, o.items[i].session) })));
}

export const stopBroadcast = () => useBroadcastRun.getState().controller?.abort();
export const clearBroadcast = () => {
  stopBroadcast();
  useBroadcastRun.setState({ run: undefined, controller: undefined });
};
