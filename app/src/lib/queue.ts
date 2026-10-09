import { create } from "zustand";

import { toastManager } from "@/components/ui/toast";
import { ApiError, type BerthEvent } from "@/lib/api";
import { guessSessionName, sessionName } from "@/lib/derive";
import { plainError } from "@/lib/errors";
import { resolve, route } from "@/lib/notifications";
import { useStore } from "@/lib/store";

// The offline prompt queue (docs/guides/offline-queue.mdx): prompts for a box that
// is away wait in the laptop agent, which types them in once it is back.
// The app only shows and manages them; delivery happens with the app closed.

export type QueueState = "queued" | "waiting" | "sending" | "failed" | "delivered";

export interface QueueItem {
  id: string;
  box: string;
  session: string;
  text: string;
  enter: boolean;
  wait: boolean;
  state: QueueState;
  error?: string;
  created: string;
  attempts?: number;
  last_attempt?: string;
  // Held behind an earlier prompt to the same session that failed.
  blocked?: boolean;
  seq: number;
}

interface QueueStore {
  items: QueueItem[];
  loaded: boolean;
  open: boolean;
  error?: string;
}

export const useQueue = create<QueueStore>()(() => ({ items: [], loaded: false, open: false }));

export const openQueue = (open = true) => useQueue.setState({ open });

// Prompts by id, so delivered and failed notifications can say which one
// after the agent has dropped it: events never carry the text.
const known = new Map<string, QueueItem>();

function setItems(items: QueueItem[]) {
  for (const it of items) known.set(it.id, it);
  useQueue.setState({ items, loaded: true, error: undefined });
  // A failed prompt's note settles once it is retried, sent or discarded.
  const failed = new Set(items.filter((i) => i.state === "failed").map((i) => i.id));
  resolve((n) => n.category === "queueFailed" && n.key.startsWith("queueFailed|") && !failed.has(n.key.slice("queueFailed|".length)));
}

export async function loadQueue() {
  const c = useStore.getState().client;
  if (!c) return;
  try {
    setItems((await c.laptop<QueueItem[] | null>("GET", "/v1/queue")) ?? []);
  } catch (err) {
    useQueue.setState({ loaded: true, error: plainError(err) });
  }
}

let reloadTimer = 0;
const reloadSoon = () => {
  window.clearTimeout(reloadTimer);
  reloadTimer = window.setTimeout(() => void loadQueue(), 120);
};

// The queue loads once the agent answers, and again whenever the status is
// refetched (every reconnect and the slow poll), so nothing missed stays.
useStore.subscribe((s, prev) => {
  if (s.client && (s.client !== prev.client || s.status !== prev.status)) reloadSoon();
});

const call = <T>(method: string, path: string, body?: unknown) => {
  const c = useStore.getState().client;
  if (!c) return Promise.reject(new Error("not connected to the Shipyard agent"));
  return c.laptop<T>(method, path, body);
};

// ---- Telling an offline box from a failed send ----------------------------

// boxOffline is true when the agent already knows the box is away, so a send
// would only wait out a dial timeout.
export function boxOffline(box: string): boolean {
  const st = useStore.getState().status?.boxes.find((b) => b.name === box)?.state;
  return st === "offline" || st === "connecting";
}

export interface SendFailure {
  // offline: the prompt never reached the box, so queueing is safe.
  // uncertain: the connection dropped mid-send; it may have arrived.
  kind: "offline" | "uncertain";
  message: string;
}

// sendFailure says whether a failed send is the box being away. The agent
// answers 503 when the request never reached the box and 502 when the
// connection dropped after it went (docs/reference/app-api.mdx); anything else (no
// such session, a before: hook) is the box's own answer, not for the queue.
export function sendFailure(err: unknown, box: string): SendFailure | undefined {
  if (err instanceof ApiError && err.status === 503) return { kind: "offline", message: `${box} can't be reached right now.` };
  if (err instanceof ApiError && err.status === 502 && !/no longer trusts/.test(err.message)) {
    return { kind: "uncertain", message: `The connection to ${box} dropped while sending, so the prompt may have arrived. Check the session before queueing it again.` };
  }
  return undefined;
}

// ---- Changes --------------------------------------------------------------

const newId = () => `app-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

export interface EnqueueOptions {
	 id?: string;
  native?: { idem_key: string; when?: "now" | "idle"; files?: { uri: string; name?: string }[]; skills?: { id: string }[] };
  box: string;
  session: string;
  text: string;
  enter?: boolean;
  // Hold it while the agent is mid-turn (default true).
  wait?: boolean;
  // Say so in a toast (default true).
  toast?: boolean;
}

// enqueue hands a prompt to the agent. The id is made here, so a retry of
// this call never queues it twice.
export async function enqueue(o: EnqueueOptions): Promise<QueueItem> {
  const it = await call<QueueItem>("POST", "/v1/queue", { id: o.id ?? newId(), box: o.box, session: o.session, text: o.text, enter: o.enter ?? true, wait: o.wait ?? true, native: o.native });
  known.set(it.id, it);
  useQueue.setState((s) => ({ items: [...s.items.filter((x) => x.id !== it.id), it].sort((a, b) => a.seq - b.seq) }));
  if (o.toast !== false) {
    toastManager.add({
      title: `Queued for when ${o.box} is back`,
      description: `${targetName(o.box, o.session)} gets it as soon as ${o.box} answers.`,
      type: "info",
      actionProps: { children: "View queue", onClick: () => openQueue() },
    });
  }
  return it;
}

export async function discard(id: string) {
  await call("DELETE", `/v1/queue/${encodeURIComponent(id)}`);
  useQueue.setState((s) => ({ items: s.items.filter((x) => x.id !== id) }));
}

export async function retry(id: string) {
  const it = await call<QueueItem>("POST", `/v1/queue/${encodeURIComponent(id)}/retry`);
  replace(it);
}

export async function retarget(id: string, box: string, session: string) {
  const it = await call<QueueItem>("PATCH", `/v1/queue/${encodeURIComponent(id)}`, { box, session });
  replace(it);
}

// sendNow types it in at once, without waiting for the agent's turn to end.
export async function sendNow(id: string): Promise<QueueItem> {
  const it = await call<QueueItem>("POST", `/v1/queue/${encodeURIComponent(id)}/send`);
  if (it.state === "delivered") useQueue.setState((s) => ({ items: s.items.filter((x) => x.id !== id) }));
  else replace(it);
  return it;
}

function replace(it: QueueItem) {
  known.set(it.id, it);
  useQueue.setState((s) => ({ items: s.items.map((x) => (x.id === it.id ? it : x)) }));
}

// ---- Naming ---------------------------------------------------------------

// targetName is what the app calls the session a prompt is for, with where
// it runs: "shop / checkout-fix · Claude Code". A session the box no longer
// lists (or one on a box that never loaded) is named from its id.
export function targetName(box: string, session: string): string {
  const d = useStore.getState().boxes[box];
  const s = d?.sessions?.find((x) => x.name === session);
  return s ? sessionName(s, { sessions: d?.sessions, locations: d?.locations, place: true }) : guessSessionName(session);
}

export const preview = (text: string, n = 80) => {
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length > n ? `${flat.slice(0, n - 1)}…` : flat;
};

// ---- Events ---------------------------------------------------------------

// handleQueueEvent keeps the list current and routes what the person
// would want to know through the notification centre.
export function handleQueueEvent(e: BerthEvent) {
  if (!e.type.startsWith("queue.")) return;
  reloadSoon();
  const d = e.data ?? {};
  const id = String(d.id ?? "");
  const box = String(d.box ?? e.box ?? "");
  const session = String(d.session ?? "");
  if (!id || !box) return;
  const text = known.get(id)?.text;
  const who = targetName(box, session);
  if (e.type === "queue.delivered") {
    route({
      category: "queueDelivered",
      title: `Queued prompt sent to ${who}`,
      detail: text ? `“${preview(text, 60)}”` : undefined,
      tone: "success",
      box,
      session,
      action: { kind: "session", box, session },
      key: `queueDelivered|${id}`,
    });
  }
  if (e.type === "queue.failed") {
    route({
      category: "queueFailed",
      title: `Couldn't deliver a queued prompt to ${who}`,
      detail: (session ? String(d.reason ?? e.error ?? "").split(session).join(who) : String(d.reason ?? e.error ?? "")) || undefined,
      tone: "error",
      box,
      session,
      label: "Open queue",
      run: () => openQueue(),
      key: `queueFailed|${id}`,
    });
  }
}
