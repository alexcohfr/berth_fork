import { useRef, useState } from "react";
import { fieldVisible, openCodeAction, useOpenCode, type FormValue, type OpenCodeForm, type OpenCodeCatalog } from "@/lib/opencode";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { boxApi } from "@/lib/api";
import { useStore } from "@/lib/store";
import { OpenCodeSettingsPanel } from "@/components/conversation/opencode-settings";

export function OpenCodeControls({ box, session, visible, onShowTerminal }: { box: string; session: string; visible: boolean; onShowTerminal(): void }) {
  const { state, error, refresh } = useOpenCode(box, session, visible);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string>();
  const [catalog, setCatalog] = useState<OpenCodeCatalog>();
  const [skill, setSkill] = useState("");
  const [skillText, setSkillText] = useState("");
  const [search, setSearch] = useState("");
  const [conversations, setConversations] = useState<{ data: { id: string; title: string }[]; cursor: { next?: string } }>();
  const [compact, setCompact] = useState<string>();
  const [settingsOpen, setSettingsOpen] = useState(false);
  const loadConversations = async (cursor?: string) => {
    const client = useStore.getState().client;
    if (!client) return;
    try {
      const result = await client.box<NonNullable<typeof conversations>>(box, "GET", `sessions/${encodeURIComponent(session)}/opencode/conversations?${new URLSearchParams({ search, ...(cursor ? { cursor } : {}) })}`);
      setConversations((old) => cursor ? { ...result, data: [...(old?.data ?? []), ...result.data] } : result);
    } catch (err) { setFailure(err instanceof Error ? err.message : String(err)); }
  };
  const skillRequest = useRef<{ payload: string; id: string }>(undefined);
  const loadCatalog = async () => {
    const client = useStore.getState().client;
    if (!client) return;
    try { setCatalog(await client.box<OpenCodeCatalog>(box, "GET", `sessions/${encodeURIComponent(session)}/opencode/catalog`)); }
    catch (err) { setFailure(err instanceof Error ? err.message : String(err)); }
  };
  const region = useRef<HTMLElement>(null);
  const act = async (action: string, body: object) => {
    if (!state || busy) return;
    setBusy(true); setFailure(undefined);
    try { await openCodeAction(box, session, state, action, body); region.current?.focus(); }
    catch (err) { setFailure(err instanceof Error ? err.message : String(err)); }
    finally { setBusy(false); refresh(); }
  };
  return <section ref={region} tabIndex={-1} aria-label="OpenCode runtime" className="mb-2 max-h-[50vh] space-y-2 overflow-y-auto text-xs">
    <div role="status" className="flex flex-wrap items-center gap-2 text-muted-foreground">
      <span>{error ? `OpenCode disconnected: ${error}` : state ? `OpenCode ${state.version} · ${state.running ? "Running" : "Connected"}` : "Connecting to OpenCode…"}</span>
      {error && <Button size="xs" variant="ghost" onClick={refresh}>Reconnect OpenCode</Button>}
      <Button size="xs" variant="ghost" onClick={onShowTerminal}>Open terminal</Button>
    </div>
    {failure && <p role="alert">{failure}</p>}
    {state?.session.revert && <div className="space-y-2 rounded-md border p-3"><p role="status">Conversation rewind staged. Files have not changed.</p><div className="flex flex-wrap gap-2"><Button size="xs" disabled={busy || !!error || state.running} onClick={() => void act("revert-commit", { id: state.session.revert!.messageID })}>Confirm conversation rewind</Button><Button size="xs" variant="outline" disabled={busy || !!error || state.running} onClick={() => void act("revert-clear", { id: state.session.revert!.messageID })}>Cancel conversation rewind</Button></div></div>}
    {state && <details onToggle={(e) => { if (e.currentTarget.open && !catalog) void loadCatalog(); }}>
      <summary className="cursor-pointer py-1">{state.session.agent ?? "Default agent"} · {state.session.model ? `${state.session.model.providerID}/${state.session.model.id}${state.session.model.variant ? `#${state.session.model.variant}` : ""}` : "Default model"} · OpenCode options</summary>
      <div className="space-y-3 rounded-md border p-3">
        <p className="break-all text-muted-foreground">Runtime on {box}: {state.directory}</p>
        <Button size="xs" variant="outline" onClick={() => void loadCatalog()}>Refresh catalogs</Button>
        {catalog && <>
           <label className="block space-y-1">Agent profile<select aria-label="OpenCode agent profile" className="block w-full rounded-md border bg-background p-2" value={state.session.agent ?? ""} disabled={busy || !!error || !state.capabilities.includes("agent")} onChange={(e) => void act("agent", { agent: e.target.value })}><option value="" disabled>Runtime default</option>{(catalog.agents ?? []).filter((a) => !a.hidden && a.mode !== "subagent").map((a) => <option value={a.id} key={a.id}>{a.name}</option>)}</select></label>
           <label className="block space-y-1">Model and variant<select aria-label="OpenCode model and variant" className="block w-full rounded-md border bg-background p-2" disabled={busy || !!error || !state.capabilities.includes("model")} value={state.session.model ? JSON.stringify({ id: state.session.model.id, providerID: state.session.model.providerID, ...(state.session.model.variant ? { variant: state.session.model.variant } : {}) }) : ""} onChange={(e) => void act("model", { model: JSON.parse(e.target.value) })}><option value="" disabled>Runtime default</option>{[...new Set((catalog.models ?? []).map((m) => m.providerID))].map((provider) => <optgroup key={provider} label={provider}>{catalog.models.filter((m) => m.providerID === provider && m.enabled).flatMap((m) => ["", ...(m.variants ?? []).map((v) => v.id)].map((variant) => <option key={`${m.id}/${variant}`} value={JSON.stringify({ id: m.id, providerID: provider, ...(variant ? { variant } : {}) })}>{m.name}{variant ? ` · ${variant}` : ""}</option>))}</optgroup>)}</select></label>
          <p className="text-muted-foreground">Changing the agent keeps the current model. Plan uses OpenCode's own Plan agent.</p>
           <p>Registered commands: {(catalog.commands ?? []).map((c) => `/${c.name}`).join(", ") || "None"}. Run them from the reply box.</p>
          <form className="space-y-2" aria-label="Use OpenCode skill" onSubmit={(e) => {
            e.preventDefault(); const client = useStore.getState().client; if (!client || !skill || busy) return;
            const payload = JSON.stringify([skill, skillText]); if (skillRequest.current?.payload !== payload) skillRequest.current = { payload, id: crypto.randomUUID() };
            setBusy(true); setFailure(undefined);
            void boxApi.send(client, box, session, skillText, true, { skills: [{ id: skill }], idem_key: skillRequest.current.id, when: "idle" }).then(() => { skillRequest.current = undefined; setSkillText(""); }, (err) => setFailure(err instanceof Error ? err.message : String(err))).finally(() => { setBusy(false); refresh(); });
          }}>
             <label className="block">Effective skills<select aria-label="OpenCode skill" value={skill} onChange={(e) => setSkill(e.target.value)} className="block w-full rounded-md border bg-background p-2"><option value="">Choose a skill…</option>{(catalog.skills ?? []).map((s) => <option key={s.id} value={s.id}>{s.name} · {s.path}</option>)}</select></label>
            <Input aria-label="Skill instruction" placeholder="Instruction for the selected skill" value={skillText} onChange={(e) => setSkillText(e.target.value)} />
             <Button size="xs" type="submit" disabled={!skill || busy || !!error || !state.capabilities.includes("skill")}>Use skill</Button>
          </form>
        </>}
        <p className="text-muted-foreground">Usage reported by OpenCode: {state.session.tokens ? `${state.session.tokens.input} input · ${state.session.tokens.output} output tokens` : "unavailable"}. Cost: ${state.session.cost.toFixed(4)} USD (provider estimate, not a subscription bill). Context occupancy unavailable.</p>
        <Button size="xs" variant="outline" disabled={busy || !!error || !!compact || !state.capabilities.includes("compact")} onClick={() => { const id = `msg_${crypto.randomUUID()}`; setCompact(id); void act("compact", { id }); }}>Compact conversation</Button>
        {(compact || state.compaction) && <p role="status">Compaction: {!compact || compact === state.compaction?.id ? state.compaction?.status : "admission unconfirmed"}. {compact && compact !== state.compaction?.id && <Button size="xs" variant="outline" disabled={busy || !!error} onClick={() => void act("compact", { id: compact })}>Retry compaction</Button>} {compact && <button type="button" onClick={() => setCompact(undefined)} className="underline">Dismiss</button>}</p>}
        {!!state.inbox?.length && <div aria-label="OpenCode inbox">{state.inbox.map((item) => <p key={item.id}>Queued {item.type}: {item.text ?? item.id}</p>)}</div>}
        <form onSubmit={(e) => { e.preventDefault(); void loadConversations(); }} className="flex flex-wrap gap-2">
          <Input aria-label="Search OpenCode conversations" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Conversations in this worktree" />
           <Button size="xs" type="submit" variant="outline" disabled={busy || !!error || !state.capabilities.includes("resume")}>Search conversations</Button>
        </form>
        {conversations?.data.map((conversation) => <div key={conversation.id} className="flex items-center justify-between gap-2"><span className="truncate">{conversation.title || conversation.id}</span><Button size="xs" variant="outline" disabled={busy || conversation.id === state.session.id} onClick={() => void act("resume", { id: conversation.id })}>Resume conversation</Button></div>)}
        {conversations?.cursor.next && <Button size="xs" variant="ghost" onClick={() => void loadConversations(conversations.cursor.next)}>More conversations</Button>}
        <details onToggle={(e) => setSettingsOpen(e.currentTarget.open)}><summary className="cursor-pointer py-2">OpenCode on this machine</summary>{settingsOpen && (state.capabilities.includes("settings") ? <OpenCodeSettingsPanel key={state.instance} box={box} session={session} state={state} editor /> : <p>Native settings are unavailable on this runtime. Use OpenCode's terminal.</p>)}</details>
      </div>
    </details>}
    {state?.permissions?.map((p) => <div key={`${state.instance}/${p.id}`} className="space-y-2 rounded-md border p-3">
      <p className="font-medium">Permission: {p.action}{p.sessionID !== state.session.id ? " · subagent" : ""}</p>
      <p className="whitespace-pre-wrap break-words">{p.resources.join("\n")}</p>
      {p.message && <p>{p.message}</p>}
      <p className="text-muted-foreground">Always saves the proposed patterns for this project. Reject refuses all pending permissions in this conversation.</p>
      {!!p.save?.length && <p>Saved patterns: {p.save.join(", ")}</p>}
      <div className="flex flex-wrap gap-2">{([ ["once", "Allow once"], ["always", "Always allow"], ["reject", "Reject"] ] as const).map(([decision, label]) => <Button key={decision} size="xs" variant="outline" disabled={busy || !!error} onClick={() => void act("permission", { id: p.id, target: p.sessionID, decision })}>{label}</Button>)}</div>
    </div>)}
    {state?.forms?.map((form) => <NativeForm key={`${state.instance}/${form.id}`} form={form} disabled={busy || !!error} onSubmit={(answer) => act("form", { id: form.id, target: form.sessionID, answer })} onShowTerminal={onShowTerminal} />)}
  </section>;
}

function NativeForm({ form, disabled, onSubmit, onShowTerminal }: { form: OpenCodeForm; disabled: boolean; onSubmit(answer: Record<string, FormValue>): Promise<void>; onShowTerminal(): void }) {
  const [values, setValues] = useState<Record<string, FormValue>>(() => Object.fromEntries(form.fields.filter((f) => f.default !== undefined || f.type === "boolean").map((f) => [f.key, f.default ?? false])));
  const [error, setError] = useState<string>();
  const fields = form.fields.filter((f) => fieldVisible(f, values));
  const unsupported = fields.some((f) => !["string", "number", "integer", "boolean", "multiselect"].includes(f.type));
  return <form aria-label={form.title} className="space-y-3 rounded-md border p-3" onSubmit={(event) => {
    event.preventDefault(); setError(undefined);
    const answer: Record<string, FormValue> = {};
    for (const field of fields) {
      const value = values[field.key];
      if (value === undefined || value === "") { if (field.required) { setError(`${field.title ?? field.key} is required`); return; } continue; }
      if (Array.isArray(value) && (value.length < (field.minItems ?? (field.required ? 1 : 0)) || value.length > (field.maxItems ?? Infinity))) { setError(`Check the number of choices for ${field.title ?? field.key}`); return; }
      if (typeof value === "number" && (!Number.isFinite(value) || (field.type === "integer" && !Number.isInteger(value)))) { setError("Enter a valid number"); return; }
      if (field.format === "date-time" && typeof value === "string") {
        const date = new Date(value);
        if (!Number.isFinite(date.getTime())) { setError("Enter a valid date and time"); return; }
        answer[field.key] = date.toISOString();
      } else answer[field.key] = value;
    }
    void onSubmit(answer);
  }}>
    <h3 className="font-medium">{form.title}</h3>
    {fields.map((field) => {
      const value = values[field.key];
      const set = (v: FormValue) => setValues((old) => ({ ...old, [field.key]: v }));
      const label = field.title ?? field.key;
      if (!["string", "number", "integer", "boolean", "multiselect"].includes(field.type)) return <p key={field.key}>{label}: continue this {field.type === "external" ? "external connection" : "unsupported field"} in OpenCode's terminal.</p>;
      return <label key={field.key} className="block space-y-1">
        <span>{label}{field.required ? " *" : ""}</span>
        {field.description && <span className="block text-muted-foreground">{field.description}</span>}
        {field.type === "boolean" ? <input type="checkbox" checked={value === true} disabled={disabled} onChange={(e) => set(e.target.checked)} className="ml-2 accent-primary" /> :
          field.type === "multiselect" ? <><select multiple aria-label={label} disabled={disabled} className="block w-full rounded-md border bg-background p-2" value={Array.isArray(value) ? value : []} onChange={(e) => set(Array.from(e.target.selectedOptions, (o) => o.value))}>{[...(field.options ?? []), ...(Array.isArray(value) ? value.filter((v) => !field.options?.some((o) => o.value === v)).map((v) => ({ value: v, label: v })) : [])].map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select>{field.custom && <Input aria-label={`${label}: add custom choice`} placeholder="Custom choice (Enter to add)" onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); const input = e.currentTarget; if (input.value.trim()) set([...new Set([...(Array.isArray(value) ? value : []), input.value.trim()])]); input.value = ""; } }} />}</> :
          field.type === "string" && field.options && !field.custom ? <select required={field.required} disabled={disabled} className="block w-full rounded-md border bg-background p-2" value={String(value ?? "")} onChange={(e) => set(e.target.value)}><option value="">Choose…</option>{field.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}</select> :
          <Input disabled={disabled} required={field.required} type={field.type === "number" || field.type === "integer" ? "number" : field.format === "uri" ? "url" : field.format === "date-time" ? "datetime-local" : field.format ?? "text"} step={field.type === "integer" ? 1 : "any"} min={field.minimum} max={field.maximum} minLength={field.minLength} maxLength={field.maxLength} pattern={field.pattern} placeholder={field.placeholder} value={String(value ?? "")} onChange={(e) => set(field.type === "number" || field.type === "integer" ? (e.target.value === "" ? "" : Number(e.target.value)) : e.target.value)} />}
      </label>;
    })}
    {error && <p role="alert">{error}</p>}
    {unsupported ? <Button type="button" size="xs" variant="outline" onClick={onShowTerminal}>Answer in terminal</Button> : <Button type="submit" size="xs" disabled={disabled}>Submit answer</Button>}
  </form>;
}
