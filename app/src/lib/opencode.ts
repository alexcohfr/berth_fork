import { useCallback, useEffect, useState } from "react";
import { useStore } from "@/lib/store";

export type FormValue = string | number | boolean | string[];
export interface OpenCodeField {
  key: string; type: string; title?: string; description?: string; required?: boolean; hidden?: boolean;
  when?: { key: string; op: "eq" | "neq"; value: string | number | boolean }[];
  default?: FormValue; options?: { value: string; label: string; description?: string }[];
  custom?: boolean; minimum?: number; maximum?: number; minLength?: number; maxLength?: number;
  minItems?: number; maxItems?: number; pattern?: string; placeholder?: string; format?: string; url?: string;
}
export interface OpenCodeForm { id: string; sessionID: string; title: string; fields: OpenCodeField[] }
export interface OpenCodePermission { id: string; sessionID: string; action: string; resources: string[]; save?: string[]; message?: string }
export interface OpenCodeCatalog {
  agents: { id: string; name: string; hidden: boolean; mode: string }[];
  models: { id: string; providerID: string; name: string; enabled: boolean; variants: { id: string }[] }[];
  commands: { name: string; description?: string }[];
  skills: { id: string; name: string; description?: string; path: string }[];
}

export interface OpenCodeState {
  version: string;
  instance: string;
  directory: string;
  running: boolean;
  capabilities: string[];
  permissions?: OpenCodePermission[];
  forms?: OpenCodeForm[];
  inbox?: { id: string; type: string; text?: string }[];
  compaction?: { id: string; status: "pending" | "running" | "completed" | "failed" };
  session: { id: string; agent?: string; outcome?: string; revert?: { messageID: string }; model?: { providerID: string; id: string; variant?: string }; cost: number; tokens?: { input: number; output: number; reasoning: number; cache: { read: number; write: number } } };
}

export async function openCodeAction(box: string, session: string, state: OpenCodeState, action: string, body: object = {}) {
  const client = useStore.getState().client;
  if (!client) throw new Error("Shipyard disconnected");
  return client.box(box, "POST", `sessions/${encodeURIComponent(session)}/opencode/${encodeURIComponent(action)}`, { ...body, instance: state.instance, session_id: state.session.id });
}

export const fieldVisible = (field: OpenCodeField, values: Record<string, FormValue>) => !field.hidden && (field.when ?? []).every((rule) => (values[rule.key] === rule.value) === (rule.op === "eq"));

export function useOpenCode(box: string, session: string, enabled: boolean) {
  const client = useStore((s) => s.client);
  const [state, setState] = useState<OpenCodeState>();
  const [error, setError] = useState<string>();
  const [revision, setRevision] = useState(0);
  const refresh = useCallback(() => setRevision((n) => n + 1), []);
  useEffect(() => {
    if (!client || !enabled) return;
    let alive = true;
    let timer: ReturnType<typeof setTimeout>;
    const ctl = new AbortController();
    const read = async () => {
      try {
        const value = await client.box<OpenCodeState>(box, "GET", `sessions/${encodeURIComponent(session)}/opencode`, undefined, ctl.signal);
        if (alive) { setState(value); setError(undefined); }
      } catch (err) { if (alive) setError(err instanceof Error ? err.message : String(err)); }
      finally { if (alive) timer = setTimeout(read, 2000); }
    };
    void read();
    return () => { alive = false; ctl.abort(); clearTimeout(timer); };
  }, [client, box, session, enabled, revision]);
  useEffect(() => { setState(undefined); setError(undefined); }, [box, session]);
  return { state, error, refresh };
}
