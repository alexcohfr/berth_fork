import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useStore } from "@/lib/store";
import { useOpenCode, type OpenCodeState } from "@/lib/opencode";
import { openUrl } from "@/lib/open-url";
import { openFile } from "@/lib/files";

interface Settings {
  sources: { type: string; path?: string }[];
  mcp: { name: string; integrationID?: string; status: { status: string } }[];
  plugins: { id: string; state: { status: string } }[];
  integrations: { id: string; name: string; methods: { id?: string; type: string; label?: string; form?: unknown[] }[]; connections: { type: string; method?: string }[] }[];
  attempts: { integration: string; attemptID: string; url: string; mode: string; status?: string }[];
}
interface TransferFile { path: string; content?: string; before?: string; etag: string; executable?: boolean }

export function OpenCodeMachineSettings({ box, session }: { box: string; session: string }) {
  const { state, error } = useOpenCode(box, session, true);
  return state ? <OpenCodeSettingsPanel key={state.instance} box={box} session={session} state={state} /> : <p role="status">{error ?? "Connecting to OpenCode…"}</p>;
}

export function OpenCodeSettingsPanel({ box, session, state, editor = false }: { box: string; session: string; state: OpenCodeState; editor?: boolean }) {
  const client = useStore((s) => s.client);
  const boxes = useStore((s) => s.boxes);
  const [settings, setSettings] = useState<Settings>();
  const [failure, setFailure] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  const [keyFor, setKeyFor] = useState("");
  const [key, setKey] = useState("");
  const [code, setCode] = useState("");
  const base = `sessions/${encodeURIComponent(session)}/opencode`;
  useEffect(() => {
    if (!client) return;
    let alive = true;
    const controller = new AbortController();
    void client.box<Settings>(box, "GET", `${base}/settings`, undefined, controller.signal).then((value) => { if (alive) setSettings(value); }, (err) => { if (alive) setFailure(err instanceof Error ? err.message : String(err)); });
    return () => { alive = false; controller.abort(); };
  }, [client, box, base, revision]);
  const action = async (body: object) => {
    if (!client || busy) return;
    setBusy(true); setFailure(undefined);
    try { await client.box(box, "POST", `${base}/settings`, { ...body, instance: state.instance }); setKey(""); setCode(""); setKeyFor(""); }
    catch (err) { setFailure(err instanceof Error ? err.message : String(err)); }
    finally { setBusy(false); setRevision((n) => n + 1); }
  };
  const [scope, setScope] = useState("project");
  const [manifest, setManifest] = useState<TransferFile[]>();
  const [selected, setSelected] = useState<string[]>([]);
  const [bundle, setBundle] = useState<TransferFile[]>();
  const [destination, setDestination] = useState("");
  const [preview, setPreview] = useState<{ box: string; session: string; instance: string; files: TransferFile[] }>();
  const [applied, setApplied] = useState(false);
  const transfer = async (step: "list" | "export" | "preview" | "apply") => {
    if (!client || busy) return;
    setBusy(true); setFailure(undefined); setApplied(false);
    try {
      if (step === "list") {
        const result = await client.box<{ files: TransferFile[] }>(box, "GET", `${base}/transfer?scope=${scope}`);
        setManifest(result.files); setSelected([]); setBundle(undefined); setPreview(undefined);
      } else if (step === "export") {
        const result = await client.box<{ files: TransferFile[] }>(box, "POST", `${base}/transfer?scope=${scope}`, { action: "export", instance: state.instance, paths: selected });
        setBundle(result.files); setPreview(undefined);
      } else if (step === "preview") {
        const [targetBox, targetSession] = JSON.parse(destination) as [string, string];
        const targetBase = `sessions/${encodeURIComponent(targetSession)}/opencode`;
        const runtime = await client.box<OpenCodeState>(targetBox, "GET", targetBase);
        const result = await client.box<{ files: TransferFile[] }>(targetBox, "POST", `${targetBase}/transfer`, { action: "preview", instance: runtime.instance, files: bundle });
        setPreview({ box: targetBox, session: targetSession, instance: runtime.instance, files: result.files });
      } else if (preview) {
        await client.box(preview.box, "POST", `sessions/${encodeURIComponent(preview.session)}/opencode/transfer`, { action: "apply", instance: preview.instance, files: preview.files });
        setApplied(true); setPreview(undefined);
      }
    } catch (err) { setFailure(err instanceof Error ? err.message : String(err)); }
    finally { setBusy(false); }
  };
  return <div className="space-y-3 text-xs" aria-label="OpenCode on this machine">
    <p>OpenCode {state.version} on {box} · <span className="break-all">{state.directory}</span></p>
    {failure && <p role="alert">{failure}</p>}
    <div className="flex flex-wrap gap-2"><Button size="xs" variant="outline" disabled={busy} onClick={() => setRevision((n) => n + 1)}>Refresh OpenCode settings</Button><Button size="xs" variant="outline" disabled={busy} onClick={() => void action({ action: "reload" })}>Reload runtime configuration</Button></div>
    <p className="text-muted-foreground">Reload cancels pending permissions and forms in this runtime. Running work uses refreshed services at the next step. Session choices stay in the conversation; user settings belong to this box's account.</p>
    {settings && <>
      <h3 className="font-medium">Configuration sources, lowest priority first</h3>
      <ul className="space-y-1">{settings.sources.map((source, i) => <li key={`${source.path}/${i}`} className="break-all">{source.type}: {source.path ?? "runtime configuration"}{editor && source.path?.startsWith(`${state.directory}/`) && <Button size="xs" variant="ghost" onClick={() => openFile(source.path!.slice(state.directory.length + 1))}>Edit project file</Button>}</li>)}</ul>
      <p className="text-muted-foreground">The effective skills are listed in conversation options. AGENTS.md is loaded by OpenCode. Plugin-injected instructions and the complete system prompt are not observable here. The instructions config field is not resolved by this OpenCode version.</p>
      {editor && <Button size="xs" variant="outline" onClick={() => openFile("opencode.jsonc")}>Open project opencode.jsonc</Button>}
      <h3 className="font-medium">MCP on {box}</h3>
      {!settings.mcp?.length && <p>No MCP servers configured.</p>}
      {settings.mcp?.map((server) => <div key={server.name} className="flex flex-wrap items-center gap-2"><span>{server.name} · {server.status.status}</span><Button size="xs" variant="outline" disabled={busy} onClick={() => void action({ action: server.status.status === "connected" ? "mcp-disconnect" : "mcp-connect", server: server.name })}>{server.status.status === "connected" ? "Disconnect MCP" : "Reconnect MCP"}</Button>{server.integrationID && <span>Connection: {server.integrationID}</span>}</div>)}
      <p className="text-muted-foreground">Tools execute on {box}. A Mac-only tool or OS permission is unavailable on a Linux box until configured there. A sleeping Mac cannot relay it.</p>
      <h3 className="font-medium">Provider and MCP connections</h3>
      {settings.integrations?.map((integration) => <div key={integration.id} className="space-y-1">
        <p>{integration.name} · {integration.connections?.length ? "connected" : "connection required"}</p>
        <div className="flex flex-wrap gap-2">{integration.methods.map((method, i) => method.form?.length || !["oauth", "key"].includes(method.type) ? <span key={i} className="text-muted-foreground">{method.label ?? method.type}: configure in OpenCode's terminal on {box}</span> : <Button key={method.id ?? i} size="xs" variant="outline" disabled={busy} onClick={() => method.type === "key" ? setKeyFor(integration.id) : void action({ action: "oauth", integration: integration.id, method: method.id })}>{method.label ?? `Connect ${integration.name}`}</Button>)}</div>
      </div>)}
      {keyFor && <form className="flex flex-wrap gap-2" onSubmit={(event) => { event.preventDefault(); void action({ action: "key", integration: keyFor, key }); }}><Input autoComplete="off" type="password" aria-label="OpenCode integration key" value={key} onChange={(e) => setKey(e.target.value)} /><Button type="submit" size="xs" disabled={busy || !key}>Save key in OpenCode</Button><Button size="xs" type="button" variant="ghost" onClick={() => { setKeyFor(""); setKey(""); }}>Cancel</Button></form>}
      {settings.attempts?.map((attempt) => <div key={attempt.attemptID} className="space-y-2" role="status"><p>{attempt.integration}: {attempt.status ?? "pending"}</p>{attempt.status !== "complete" && <div className="flex flex-wrap gap-2"><Button size="xs" variant="outline" onClick={() => void openUrl(attempt.url)}>Continue sign-in</Button>{attempt.mode === "code" && <Input type="password" autoComplete="off" aria-label="OpenCode authorization code" value={code} onChange={(e) => setCode(e.target.value)} />}<Button size="xs" disabled={busy} onClick={() => void action({ action: "oauth-complete", integration: attempt.integration, attempt: attempt.attemptID, code })}>Complete sign-in</Button><Button size="xs" variant="ghost" disabled={busy} onClick={() => void action({ action: "oauth-cancel", integration: attempt.integration, attempt: attempt.attemptID })}>Cancel sign-in</Button></div>}</div>)}
      <h3 className="font-medium">Server plugins</h3><p>{settings.plugins?.map((plugin) => `${plugin.id || "Server plugin"}: ${plugin.state.status}`).join(" · ") || "None"}</p>
      <p className="text-muted-foreground">Server plugins run in OpenCode. Their terminal UI extensions remain available in the terminal.</p>
    </>}
    <details><summary className="cursor-pointer py-2">Transfer selected configuration</summary><div className="space-y-3">
      <p>One-time transfer of rules, agents, commands and skill files to a destination project. Credentials, session databases and provider config are excluded. Reconnect integrations on the destination.</p>
      <label className="block">Source scope<select aria-label="Transfer source scope" disabled={busy} value={scope} onChange={(e) => { setScope(e.target.value); setManifest(undefined); setBundle(undefined); setPreview(undefined); }} className="ml-2 rounded border bg-background p-1"><option value="project">This project</option><option value="user">This box's user</option></select></label>
      <Button size="xs" variant="outline" disabled={busy} onClick={() => void transfer("list")}>List portable files</Button>
      {manifest?.map((file) => <label key={file.path} className="flex items-center gap-2"><input type="checkbox" disabled={busy} checked={selected.includes(file.path)} onChange={(e) => setSelected((old) => e.target.checked ? [...old, file.path] : old.filter((p) => p !== file.path))} />{file.path}</label>)}
      {manifest && <Button size="xs" disabled={busy || !selected.length} onClick={() => void transfer("export")}>Prepare selected files</Button>}
      {bundle?.map((file, i) => <details key={i}><summary>{file.path}</summary><label className="block">Destination path<Input value={file.path} disabled={busy} onChange={(e) => { setBundle((old) => old?.map((v, j) => i === j ? { ...v, path: e.target.value } : v)); setPreview(undefined); }} /></label><label className="block">Content and relative paths<textarea aria-label={`Transfer content ${i + 1}`} className="mt-1 min-h-32 w-full rounded border bg-background p-2 font-mono" value={file.content ?? ""} disabled={busy} onChange={(e) => { setBundle((old) => old?.map((v, j) => i === j ? { ...v, content: e.target.value } : v)); setPreview(undefined); }} /></label></details>)}
      {bundle && <><label className="block">Destination project<select aria-label="Transfer destination" value={destination} disabled={busy} onChange={(e) => { setDestination(e.target.value); setPreview(undefined); }} className="block w-full rounded border bg-background p-2"><option value="">Choose a running OpenCode task…</option>{Object.entries(boxes).flatMap(([boxName, value]) => (value.sessions ?? []).filter((s) => !s.exited && (s.preset === "opencode" || s.agent === "opencode") && !(boxName === box && s.name === session)).map((s) => <option key={`${boxName}/${s.name}`} value={JSON.stringify([boxName, s.name])}>{boxName} · {s.dir} · {s.name}</option>))}</select></label><Button size="xs" variant="outline" disabled={busy || !destination} onClick={() => void transfer("preview")}>Preview destination changes</Button></>}
      {preview && <><p>Apply to {preview.box} / {preview.session}. Each file is replaced atomically after checking its preview version.</p>{preview.files.map((file) => <details key={file.path}><summary>{file.path} · {file.etag === "missing" ? "new" : "replace"}{file.executable ? " · executable" : ""}</summary><p>Before</p><pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded bg-muted p-2">{file.before || "(new file)"}</pre><p>After</p><pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded bg-muted p-2">{file.content}</pre></details>)}<Button size="xs" disabled={busy} onClick={() => void transfer("apply")}>Apply reviewed transfer</Button></>}
      {applied && <p role="status">Files transferred. Review them on the destination, then reload its OpenCode configuration.</p>}
    </div></details>
  </div>;
}
