// Installed by berth: hook opencode (V2).
// Only identifiers and the working directory are sent to Shipyard's event bus.
import { execFile } from "node:child_process";

export default {
  id: "shipyard",
  setup(ctx) {
    // Shipyard launches a private server per pane. Never attribute activity
    // from a shared service (which may have inherited an old pane's env).
    // V2's --standalone client spawns `serve --stdio` for the plugin host.
    if (!process.env.BERTH_SESSION || (!process.argv.includes("--stdio") && !process.env.BERTH_OPENCODE_RUNTIME)) return;
    const controller = new AbortController();
    const report = (event, session) => new Promise((resolve) => {
      const payload = JSON.stringify({ cwd: session.location.directory, session_id: session.id });
      execFile(__BERTH_BIN__, ["hook", "opencode", event, payload],
        { timeout: 2000, maxBuffer: 4096 }, () => resolve());
    });
    const events = new Map([
      ["session.created", "session.created"],
      ["session.execution.started", "session.busy"],
      ["session.execution.succeeded", "session.idle"],
      ["session.execution.interrupted", "session.idle"],
      ["session.execution.failed", "session.error"],
      ["permission.asked", "permission.asked"],
      ["permission.replied", "permission.replied"],
      ["form.created", "form.created"],
      ["form.replied", "form.replied"],
      ["form.cancelled", "form.cancelled"],
    ]);
    const pending = (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
          const hook = events.get(event.type);
          const id = event.data?.sessionID ?? event.data?.form?.sessionID;
          if (!hook || !id) continue;
          try {
            const session = await ctx.session.get({ sessionID: id });
            // The bus includes other locations and child sessions. A helper
            // finishing must not mark the parent's turn as done.
            const root = process.env.BERTH_OPENCODE_SESSION;
            if ((root ? session.id !== root : session.parentID) || session.location.directory !== ctx.location.directory) continue;
            await report(hook, session);
          } catch { /* A removed session or failed hook must not fail OpenCode. */ }
        }
      } catch (err) {
        if (!controller.signal.aborted) console.error("Shipyard status subscription ended:", err);
      }
    })();
    return async () => { controller.abort(); await pending; };
  },
};
