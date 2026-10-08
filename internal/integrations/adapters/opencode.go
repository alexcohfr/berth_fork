package adapters

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// OpenCode reports through a small plugin berth installs, which runs the
// hook for the bus events below with {"cwd", "session_id"}.
var OpenCode = register(&Adapter{
	Name: "opencode",
	Caps: Caps{Ready: true, Started: true, Waiting: true, Finished: true, Via: "plugin"},
	Translate: func(hook string, in Payload) (string, map[string]any, bool) {
		d := map[string]any{"path": in.Str("cwd"), "agent_session_id": in.Str("session_id")}
		switch hook {
		case "session.created":
			return Ready, d, true
		case "session.busy":
			d["signal"] = "prompt"
			return Started, d, true
		case "permission.updated", "permission.asked":
			d["reason"] = "permission"
			return Waiting, d, true
		case "form.created":
			d["reason"] = "question"
			return Waiting, d, true
		case "permission.replied", "form.replied", "form.cancelled":
			d["signal"] = "tool"
			return Started, d, true
		case "session.idle":
			return Finished, d, true
		case "session.error":
			d["status"] = "error"
			return Finished, d, true
		}
		return "", nil, false
	},
})

// OpenCodePlugin is the plugin file, for the berth binary at bin.
func OpenCodePlugin(bin string) string {
	return strings.ReplaceAll(openCodePlugin, "__BERTH_BIN__", jsString(bin))
}

//go:embed opencode.js
var openCodePlugin string

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
