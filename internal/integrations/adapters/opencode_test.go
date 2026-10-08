package adapters

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenCodeV2PluginReportsOnlyItsTopLevelSession(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "hook with spaces")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s %s\\n' \"$3\" \"$4\" >>\"$TEST_HOOKS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(dir, "plugin.mjs")
	if err := os.WriteFile(plugin, []byte(OpenCodePlugin(bin)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
const plugin = (await import(pathToFileURL(process.env.TEST_PLUGIN))).default;
assert.equal(plugin.setup({}), undefined);
process.argv.push("--stdio");
let signal;
let finish;
const done = new Promise(resolve => finish = resolve);
const events = [
  ["session.created", "root"], ["session.execution.started", "root"],
  ["session.execution.succeeded", "child"], ["session.execution.succeeded", "elsewhere"],
  ["permission.asked", "root"], ["permission.replied", "root"],
  ["form.created", "root"], ["form.replied", "root"], ["session.execution.succeeded", "root"],
  ["session.execution.interrupted", "root"], ["session.execution.failed", "root"],
];
const cleanup = plugin.setup({
  location: { directory: "/acme" },
  session: { get: async ({sessionID}) => ({ id: sessionID, parentID: sessionID === "child" ? "root" : undefined, location: { directory: sessionID === "elsewhere" ? "/other" : "/acme" } }) },
  event: { subscribe: async function* (options) {
    signal = options.signal;
    for (const [type, id] of events) yield {type, data: type === "form.created" ? {form: {sessionID: id}} : {sessionID: id}};
    finish();
  } },
});
await done;
await cleanup();
assert.equal(signal.aborted, true);
const lines = (await readFile(process.env.TEST_HOOKS, "utf8")).trim().split("\n");
assert.deepEqual(lines.map(line => line.split(" ")[0]), ["session.created", "session.busy", "permission.asked", "permission.replied", "form.created", "form.replied", "session.idle", "session.idle", "session.error"]);
for (const line of lines) assert.deepEqual(JSON.parse(line.slice(line.indexOf(" ") + 1)), {cwd: "/acme", session_id: "root"});
delete process.env.BERTH_SESSION;
assert.equal(plugin.setup({}), undefined);
`)
	cmd.Env = append(os.Environ(), "BERTH_SESSION=acme-opencode", "TEST_PLUGIN="+plugin, "TEST_HOOKS="+filepath.Join(dir, "hooks"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("plugin: %v\n%s", err, out)
	}
}
