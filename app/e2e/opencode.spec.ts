import { expect, mockOnly, test } from "./fixtures";
import { fakeAgent, SESSION } from "./fake-agent";
type OpenCodeState = {
  version: string; instance: string; directory: string; running: boolean; capabilities: string[];
  session: { id: string; cost: number; agent?: string; model?: { providerID: string; id: string; variant?: string }; revert?: { messageID: string } };
  compaction?: { id: string; status: string };
};

test("OpenCode settings resume sign-in and apply only a reviewed destination transfer", async ({ app, page }) => {
  mockOnly();
  const agent = await fakeAgent();
  agent.session = { agent: "opencode", preset: "opencode", command: "opencode mini", agent_state: "running" };
  const base = `${agent.url}/v1/boxes/devl/api/sessions`;
  const source: OpenCodeState = { version: "2.0.18", instance: "acme-source", directory: "/w/shop-fix", running: true, capabilities: ["settings"], session: { id: "ses_acme", cost: 0 } };
  let pending = true;
  const actions: Record<string, unknown>[] = [];
  await page.route(base, (route) => route.fulfill({ json: [
    { name: SESSION, title: "Acme source", dir: "/w/shop-fix", location: "shop/fix", created: new Date().toISOString(), ...agent.session },
    { name: "acme-destination", title: "Acme destination", dir: "/w/shop", location: "shop", created: new Date().toISOString(), ...agent.session },
  ] }));
  await page.route(`${base}/${SESSION}/opencode`, (route) => route.fulfill({ json: source }));
  await page.route(`${base}/acme-destination/opencode`, (route) => route.fulfill({ json: { ...source, instance: "acme-target", directory: "/w/shop", session: { id: "ses_target", cost: 0 } } }));
  await page.route(`${base}/*/opencode/**`, (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/catalog")) return route.fulfill({ json: { agents: [], models: [], commands: [], skills: [] } });
    if (path.endsWith("/settings") && request.method() === "GET") return route.fulfill({ json: { sources: [], mcp: [], plugins: [], integrations: [], attempts: pending ? [{ integration: "acme", attemptID: "attempt_acme", mode: "code", url: "https://example.com/authorize", status: "pending" }] : [] } });
    if (path.endsWith("/transfer") && request.method() === "GET") return route.fulfill({ json: { files: [{ path: "AGENTS.md", etag: "source" }] } });
    const body = request.postDataJSON(); actions.push(body);
    if (body.action === "oauth-complete") { pending = false; return route.fulfill({ json: { accepted: true } }); }
    if (body.action === "export") return route.fulfill({ json: { files: [{ path: "AGENTS.md", content: "Acme rules", etag: "source" }] } });
    if (body.action === "preview") return route.fulfill({ json: { files: [{ path: "AGENTS.md", content: "Acme rules", before: "Old Acme rules", etag: "target-revision" }] } });
    return route.fulfill({ json: { accepted: true } });
  });
  try {
    await app.open({ agent, params: { view: "conversation" } });
    await app.openWorktree("devl/fix");
    await page.getByText(/OpenCode options$/).click();
    await page.getByText("OpenCode on this machine", { exact: true }).click();
    await page.getByLabel("OpenCode authorization code").fill("acme-code");
    await page.getByRole("button", { name: "Complete sign-in" }).click();
    await expect(page.getByRole("button", { name: "Complete sign-in" })).toHaveCount(0);
    expect(actions[0]).toMatchObject({ instance: "acme-source", integration: "acme", attempt: "attempt_acme", code: "acme-code" });
    await page.getByText("Transfer selected configuration", { exact: true }).click();
    await page.getByRole("button", { name: "List portable files" }).click();
    await page.getByRole("checkbox", { name: "AGENTS.md", exact: true }).check();
    await page.getByRole("button", { name: "Prepare selected files" }).click();
    await page.getByRole("combobox", { name: "Transfer destination" }).selectOption(JSON.stringify(["devl", "acme-destination"]));
    await page.getByRole("button", { name: "Preview destination changes" }).click();
    await expect(page.getByText("AGENTS.md · replace", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Apply reviewed transfer" }).click();
    await expect(page.getByRole("status").filter({ hasText: "Files transferred." })).toBeVisible();
    expect(actions.at(-1)).toMatchObject({ instance: "acme-target", action: "apply", files: [{ path: "AGENTS.md", etag: "target-revision", content: "Acme rules" }] });
    const storage = await page.evaluate(() => JSON.stringify([localStorage, sessionStorage]));
    expect(storage).not.toContain("acme-code");
  } finally { await agent.close(); }
});

test("OpenCode permissions and conditional typed forms keep their request identity", async ({ app, page }) => {
  mockOnly();
  const agent = await fakeAgent();
  agent.session = { agent: "opencode", preset: "opencode", command: "opencode mini", agent_state: "waiting" };
  const state = { version: "2.0.18", instance: "acme-instance", directory: "/w/shop-fix", running: true, capabilities: ["permission", "form"], session: { id: "ses_acme", cost: 0 }, permissions: [{ id: "per_acme", sessionID: "ses_acme", action: "shell", resources: ["pnpm test"], save: ["pnpm test *"] }], forms: [{ id: "frm_acme", sessionID: "ses_acme", title: "Acme release", fields: [{ key: "release", type: "boolean", title: "Release now", default: false }, { key: "count", type: "integer", title: "Retries", minimum: 1, maximum: 5, required: true, when: [{ key: "release", op: "eq", value: true }] }, { key: "regions", type: "multiselect", title: "Regions", required: true, minItems: 2, options: [{ value: "eu", label: "Europe" }, { value: "ca", label: "Canada" }] }] }] };
  const actions: Record<string, unknown>[] = [];
  await page.route(`${agent.url}/v1/boxes/devl/api/sessions/${SESSION}/opencode`, (route) => route.fulfill({ json: state }));
  await page.route(`${agent.url}/v1/boxes/devl/api/sessions/${SESSION}/opencode/*`, (route) => {
    actions.push(route.request().postDataJSON());
    if (route.request().url().endsWith("/permission")) state.permissions = [];
    else state.forms = [];
    return route.fulfill({ json: { accepted: true } });
  });
  try {
    await app.open({ agent, params: { view: "conversation" } });
    await app.openWorktree("devl/fix");
    await page.getByRole("button", { name: "Allow once", exact: true }).click();
    await expect(page.getByText("Permission: shell")).toHaveCount(0);
    expect(actions[0]).toMatchObject({ instance: "acme-instance", session_id: "ses_acme", id: "per_acme", decision: "once" });
    await expect(page.getByRole("spinbutton", { name: "Retries" })).toHaveCount(0);
    await page.getByRole("checkbox", { name: "Release now" }).check();
    await page.getByRole("spinbutton", { name: "Retries" }).fill("3");
    await page.getByRole("listbox", { name: "Regions" }).selectOption(["eu", "ca"]);
    await page.getByRole("button", { name: "Submit answer" }).click();
    await expect(page.getByRole("form", { name: "Acme release" })).toHaveCount(0);
    expect(actions[1]).toMatchObject({ id: "frm_acme", answer: { release: true, count: 3, regions: ["eu", "ca"] } });
    await expect(page.getByRole("region", { name: "OpenCode runtime" })).toBeFocused();
  } finally { await agent.close(); }
});

test("OpenCode sends natively, stops and reconnects without terminal input", async ({ app, page }) => {
  mockOnly();
  const agent = await fakeAgent();
  agent.session = { agent: "opencode", preset: "opencode", command: "opencode mini", agent_state: "running" };
  agent.capabilities.push("controls");
  agent.transcript = () => ({ body: { source: "opencode", items: [{ kind: "user", id: "msg_acme", text: "Acme task" }], next: 1, crew: [] } });
  let connected = true;
  let stopped = false;
  await page.route(`${agent.url}/v1/boxes/devl/api/sessions/${SESSION}/opencode`, (route) => route.fulfill(connected ? { json: { version: "2.0.18", instance: "acme", directory: "/w/shop-fix", running: !stopped, capabilities: ["prompt", "interrupt"], session: { id: "ses_acme", cost: 0 } } } : { status: 503, json: { error: "Owner unavailable" } }));
  await page.route(`${agent.url}/v1/boxes/devl/api/sessions/${SESSION}/interrupt`, (route) => { stopped = true; return route.fulfill({ json: { sent: true, stopped: true } }); });
  try {
    await app.open({ agent, params: { view: "conversation" } });
    await app.openWorktree("devl/fix");
    await expect(page.getByRole("region", { name: "OpenCode runtime" })).toContainText("OpenCode 2.0.18");
    await app.composer.getByRole("textbox", { name: "Reply" }).fill("Acme next instruction");
    await app.composer.getByRole("textbox", { name: "Reply" }).press("Enter");
    await expect.poll(() => agent.sends.length).toBe(1);
    expect(agent.sends[0]).toMatchObject({ text: "Acme next instruction", idem_key: expect.any(String) });
    await page.getByRole("button", { name: "Stop OpenCode", exact: true }).click();
    await expect.poll(() => stopped).toBe(true);
    connected = false;
    await expect(page.getByRole("status").filter({ hasText: "OpenCode disconnected" })).toBeVisible();
    connected = true;
    await page.getByRole("button", { name: "Reconnect OpenCode" }).click();
    await expect(page.getByRole("region", { name: "OpenCode runtime" })).toContainText("Connected");
    expect(agent.calls.some((c) => c.endsWith("/keys"))).toBe(false);
  } finally { await agent.close(); }
});

test("OpenCode options preserve native choices and structured uploaded files", async ({ app, page }) => {
  mockOnly();
  const agent = await fakeAgent();
  agent.session = { agent: "opencode", preset: "opencode", command: "opencode mini", agent_state: "running" };
  agent.transcript = () => ({ body: { source: "opencode", items: [{ kind: "user", id: "msg_acme", text: "Acme task" }], next: 1, crew: [] } });
  const state: OpenCodeState = { version: "2.0.18", instance: "acme", directory: "/w/shop-fix", running: true, capabilities: ["agent", "model", "skill", "files", "compact", "settings"], session: { id: "ses_acme", cost: 0, agent: "build", model: { providerID: "acme", id: "model" } } };
  const actions: Record<string, unknown>[] = [];
  const base = `${agent.url}/v1/boxes/devl/api/sessions/${SESSION}`;
  await page.route(`${base}/opencode`, (route) => route.fulfill({ json: state }));
  await page.route(`${base}/opencode/**`, (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/catalog")) return route.fulfill({ json: { agents: [{ id: "build", name: "Build" }, { id: "plan", name: "Plan" }], models: [{ id: "model", providerID: "acme", name: "Acme model", enabled: true, variants: [{ id: "deep" }] }], commands: [{ name: "acme" }], skills: [{ id: "skill_acme", name: "Acme skill", path: "/w/shop-fix/.opencode/skills/acme/SKILL.md" }] } });
    if (path.endsWith("/settings") && route.request().method() === "GET") return route.fulfill({ json: { sources: [{ type: "document", path: "/w/shop-fix/opencode.jsonc" }], mcp: [{ name: "Acme MCP", status: { status: "needs_auth" } }], integrations: [], plugins: [], attempts: [] } });
    const body = route.request().postDataJSON(); actions.push(body);
    if (path.endsWith("/agent")) state.session.agent = body.agent;
    if (path.endsWith("/model")) state.session.model = body.model;
    if (path.endsWith("/compact")) state.compaction = { id: body.id, status: "completed" };
    return route.fulfill({ json: { accepted: true } });
  });
  await page.route(`${base}/attachments?*`, (route) => route.fulfill({ json: { path: "/w/shop-fix/.berth/attachments/acme été %20 #1.png", name: "acme été %20 #1.png", type: "image/png", size: 8 } }));
  try {
    await app.open({ agent, params: { view: "conversation" } });
    await app.openWorktree("devl/fix");
    await page.getByText(/OpenCode options$/).click();
    const profile = page.getByRole("combobox", { name: "OpenCode agent profile" });
    await profile.focus(); await profile.selectOption("plan");
    await expect(profile).toHaveValue("plan");
    expect(actions[0]).toMatchObject({ agent: "plan", instance: "acme", session_id: "ses_acme" });
    const model = page.getByRole("combobox", { name: "OpenCode model and variant" });
    await model.selectOption({ label: "Acme model · deep" });
    await expect(model).toHaveValue(JSON.stringify({ id: "model", providerID: "acme", variant: "deep" }));
    await page.getByRole("combobox", { name: "OpenCode skill", exact: true }).selectOption("skill_acme");
    await page.getByRole("textbox", { name: "Skill instruction" }).fill("Acme skill instruction");
    await page.getByRole("button", { name: "Use skill", exact: true }).click();
    await expect.poll(() => agent.sends.length).toBe(1);
    expect(agent.sends[0]).toMatchObject({ text: "Acme skill instruction", skills: [{ id: "skill_acme" }] });
    await page.getByRole("button", { name: "Compact conversation", exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: "Compaction: completed" })).toBeVisible();
    await page.getByText("OpenCode on this machine", { exact: true }).click();
    await expect(page.getByLabel("OpenCode on this machine")).toContainText("needs_auth");
    await expect(page.getByLabel("OpenCode on this machine")).toContainText("Mac-only tool");
    await app.composer.getByRole("textbox", { name: "Reply" }).evaluate((element) => {
      const data = new DataTransfer();
      data.items.add(new File([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])], "acme été %20 #1.png", { type: "image/png" }));
      element.dispatchEvent(new DragEvent("drop", { dataTransfer: data, bubbles: true, cancelable: true }));
    });
    await expect(app.composer.getByTestId("attachment-chip")).toHaveAttribute("data-state", "ready");
    await app.composer.getByRole("textbox", { name: "Reply" }).fill("Inspect this image");
    await app.composer.getByRole("textbox", { name: "Reply" }).press("Enter");
    await expect.poll(() => agent.sends.length).toBe(2);
    expect(agent.sends[1]).toMatchObject({ text: "Inspect this image", files: [{ uri: "file:///w/shop-fix/.berth/attachments/acme%20%C3%A9t%C3%A9%20%2520%20%231.png", name: "acme été %20 #1.png" }] });
  } finally { await agent.close(); }
});

test("OpenCode rewind stays reversible and uses the selected native message boundary", async ({ app, page }) => {
  mockOnly();
  const agent = await fakeAgent();
  agent.session = { agent: "opencode", preset: "opencode", command: "opencode mini", agent_state: "idle" };
  agent.capabilities.push("history");
  agent.transcript = () => ({ body: { source: "opencode", items: [{ kind: "user", id: "msg_acme", text: "Acme rewind boundary" }, { kind: "text", id: "msg_reply:0", text: "Acme reply" }], next: 2, crew: [] } });
  const state: OpenCodeState = { version: "2.0.18", instance: "acme", directory: "/w/shop-fix", running: false, capabilities: ["revert"], session: { id: "ses_acme", cost: 0 } };
  const actions: string[] = [];
  const base = `${agent.url}/v1/boxes/devl/api/sessions/${SESSION}`;
  await page.route(`${base}/opencode`, (route) => route.fulfill({ json: state }));
  await page.route(`${base}/opencode/revert-*`, (route) => {
    const body = route.request().postDataJSON();
    expect(body).toMatchObject({ id: "msg_acme", instance: "acme", session_id: "ses_acme" });
    const action = new URL(route.request().url()).pathname.split("/").pop()!; actions.push(action);
    if (action === "revert-preview") return route.fulfill({ json: { text: "Acme rewind boundary", reason: "File restoration is disabled", files: false } });
    state.session.revert = action === "revert-stage" ? { messageID: "msg_acme" } : undefined;
    return route.fulfill({ json: { accepted: true } });
  });
  try {
    await app.open({ agent, params: { view: "conversation" } });
    await app.openWorktree("devl/fix");
    await app.chat.locator('[data-kind="user"]').filter({ hasText: "Acme rewind boundary" }).hover();
    await page.getByRole("button", { name: "Rewind to here", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Files affected: none");
    await dialog.getByRole("button", { name: "Stage conversation rewind" }).click();
    await expect(dialog).toHaveCount(0);
    await page.getByRole("button", { name: "Cancel conversation rewind" }).click();
    await expect(page.getByText("Conversation rewind staged. Files have not changed.")).toHaveCount(0);
    expect(actions).toEqual(["revert-preview", "revert-stage", "revert-clear"]);
    expect(agent.calls.some((call) => call.endsWith("/keys"))).toBe(false);
  } finally { await agent.close(); }
});

test("OpenCode reconnect bridges missed history pages and resets a changed generation", async ({ app, page }) => {
  mockOnly();
  const agent = await fakeAgent();
  agent.session = { agent: "opencode", preset: "opencode", command: "opencode mini", agent_state: "running" };
  agent.capabilities.push("history");
  let count = 10;
  let gen = "ses_acme:original";
  const cursors: string[] = [];
  agent.transcript = (query) => {
    const cursor = query.get("cursor");
    if (cursor) cursors.push(cursor);
    const end = cursor ? Number(cursor.replace("acme-", "")) : count;
    const start = Math.max(0, end - 100);
    return { body: { source: "opencode", file: "ses_acme", gen, cursor: start ? `acme-${start}` : null, next: end, crew: [], items: Array.from({ length: end - start }, (_, i) => ({ id: `msg_acme_${start + i}`, kind: "user", text: `Acme history message ${start + i}` })) } };
  };
  await page.route(`${agent.url}/v1/boxes/devl/api/sessions/${SESSION}/opencode`, (route) => route.fulfill({ json: { version: "2.0.18", instance: "acme", directory: "/w/shop-fix", running: true, capabilities: ["history"], session: { id: "ses_acme", cost: 0 } } }));
  try {
    await app.open({ agent, params: { view: "conversation" } });
    await app.openWorktree("devl/fix");
    const message = (n: number) => app.chat.locator('[data-kind="user"]').filter({ hasText: new RegExp(`Acme history message ${n}\\b`) });
    await expect(message(9)).toBeVisible();
    count = 260;
    agent.dropStreams();
    await expect.poll(() => cursors).toEqual(expect.arrayContaining(["acme-160", "acme-60"]));
    await expect(message(259)).toBeVisible();
    count = 3; gen = "ses_acme:rewound";
    agent.event({ type: "transcript.changed", data: { session: SESSION, instance: "acme" } });
    await expect(message(2)).toBeVisible();
    await expect(message(259)).toHaveCount(0);
    for (let i = 0; i < 3; i++) await expect(message(i)).toHaveCount(1);
  } finally { await agent.close(); }
});

test("The running-agents composer sends OpenCode files as native attachments", async ({ app, page }) => {
  mockOnly();
  const agent = await fakeAgent();
  agent.session = { agent: "opencode", preset: "opencode", command: "opencode mini", agent_state: "finished" };
  await page.route(`${agent.url}/v1/boxes/devl/api/sessions/${SESSION}/attachments?*`, (route) => route.fulfill({ json: { path: "/w/shop-fix/.berth/attachments/acme.png", name: "acme.png", type: "image/png", size: 8 } }));
  try {
    await app.open({ agent });
    await page.getByRole("button", { name: "Home", exact: true }).click();
    const composer = page.getByTestId("task-composer");
    await composer.getByRole("tab", { name: /Running agents/ }).click();
    await composer.getByRole("button", { name: "Send to: Pick agents", exact: true }).click();
    await page.getByRole("menuitemcheckbox").filter({ hasText: "Fix the flaky checkout test" }).click();
    await page.keyboard.press("Escape");
    const input = composer.getByRole("textbox", { name: "What to tell them" });
    await input.fill("Acme shared image");
    await input.evaluate((element) => {
      const data = new DataTransfer();
      data.items.add(new File([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])], "acme.png", { type: "image/png" }));
      element.dispatchEvent(new DragEvent("drop", { dataTransfer: data, bubbles: true, cancelable: true }));
    });
    await expect(composer.getByTestId("attachment-chip")).toHaveAttribute("data-state", "ready");
    await composer.getByRole("button", { name: "Send", exact: true }).click();
    await expect.poll(() => agent.sends.length).toBe(1);
    expect(agent.sends[0]).toMatchObject({ text: "Acme shared image", when: "idle", idem_key: expect.any(String), files: [{ uri: "file:///w/shop-fix/.berth/attachments/acme.png" }] });
  } finally { await agent.close(); }
});
