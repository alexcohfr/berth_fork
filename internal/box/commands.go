package box

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// GET /v1/sessions/{name}/commands is what the session's agent takes after
// a "/": its own commands, the person's and the repository's custom
// commands, skills, and plugins' commands and skills, for the chat's
// autocomplete. GET /v1/sessions/{name}/files?q= lists the worktree's files
// for "@" mentions. Both are read from disk, cached per session for a
// minute and capped; nothing is kept.

// Command is one thing the agent takes after a "/".
type Command struct {
	// Name is what is typed, with its slash: "/model", "/vercel:deploy".
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Kind is builtin, custom, skill, plugin or mcp.
	Kind string `json:"kind"`
	// Args hints at what follows it ("[model]").
	Args string `json:"args,omitempty"`
	// Aliases are other names the agent takes for it ("/cost" for /usage).
	Aliases []string `json:"aliases,omitempty"`
	// Local commands are handled by the agent's own program (a setting, a
	// screen, a figure) rather than sent to its model as a prompt, so they
	// never start a turn.
	Local bool `json:"local,omitempty"`
	// Screen commands open the agent's own interactive screen (a picker or
	// a dialog), which the chat shows as a live terminal.
	Screen bool `json:"screen,omitempty"`
	// Source is where a custom command or skill was found: user, project
	// or the plugin's name.
	Source string `json:"source,omitempty"`
}

// CommandCatalog is a session's commands and what its prefixes do.
type CommandCatalog struct {
	Agent    string    `json:"agent"`
	Version  string    `json:"version,omitempty"`
	Commands []Command `json:"commands"`
	// Prefixes are the other characters the agent reads at the start of a
	// prompt: "!" (a shell command), "#" when it saves to memory.
	Prefixes map[string]string `json:"prefixes,omitempty"`
	// Notes says what is not listed, and why.
	Notes     []string `json:"notes,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

const (
	maxCommands    = 600
	maxCommandDesc = 240
	commandsTTL    = time.Minute
)

// builtin is one row of an agent's own command table.
type builtin struct {
	name, desc, args string
	aliases          []string
	// prompt marks the commands that send a prompt to the model (and so
	// start a turn); the rest are local.
	prompt bool
	// screen marks the commands that open an interactive screen.
	screen bool
}

// claudeBuiltins is Claude Code's own commands, as v2.1.289 lists them.
//
// How it was captured (do it again on an upgrade): start `claude` in a
// throwaway tmux (tmux -L capture new-session -d -x 200 -y 60 claude), type
// "/", then press Down through the whole menu, capturing the pane each time
// (tmux capture-pane -p) and keeping the "/name  description" lines; type
// each candidate alias (/cost, /quit, /reset…) to read "/usage (cost)".
// Skills and plugins in that menu come from disk and are left out here;
// bundled skills (simplify, loop…) stay, as every install has them. prompt
// is set for the ones that send a prompt to the model; screen for the ones
// that open their own picker or dialog.
var claudeBuiltins = []builtin{
	{name: "add-dir", desc: "Add a new working directory", args: "<path>"},
	{name: "advisor", desc: "Let Claude consult a stronger model at key moments", screen: true},
	{name: "artifacts", desc: "Browse your published and shared artifacts", screen: true},
	{name: "autocompact", desc: "Set how full the context gets before auto-summarizing", screen: true},
	{name: "background", desc: "Send this session to the background and free the terminal"},
	{name: "batch", desc: "Research and plan a large-scale change, then execute it in parallel across isolated worktrees", args: "<instruction>", prompt: true},
	{name: "branch", desc: "Create a branch of the current conversation at this point"},
	{name: "btw", desc: "Ask a quick side question without interrupting the main conversation", args: "<question>", screen: true},
	{name: "bug", desc: "Report a bug or share your conversation", screen: true},
	{name: "cd", desc: "Move this session to a new working directory", args: "<path>"},
	{name: "claude-api", desc: "Reference for the Claude API and Anthropic SDK", prompt: true},
	{name: "clear", desc: "Start a new session with empty context; the previous one stays resumable", aliases: []string{"reset", "new"}},
	{name: "code-review", desc: "Review the current diff for correctness bugs", args: "[target]", aliases: []string{"review"}, prompt: true},
	{name: "color", desc: "Set the prompt bar color for this session", args: "[color]"},
	{name: "compact", desc: "Free up context by summarizing the conversation so far", args: "[instructions]"},
	{name: "config", desc: "Open settings", aliases: []string{"settings"}, screen: true},
	{name: "context", desc: "Visualize current context usage"},
	{name: "copy", desc: "Copy Claude's last response to clipboard", args: "[N]"},
	{name: "debug", desc: "Enable debug logging for this session and help diagnose issues", prompt: true},
	{name: "desktop", desc: "Continue the current session in Claude Desktop", aliases: []string{"app"}},
	{name: "diff", desc: "Toggle the diff panel showing uncommitted changes", screen: true},
	{name: "doctor", desc: "Health-check the Claude Code setup and fix issues", screen: true},
	{name: "effort", desc: "Set effort level for model usage", args: "[low|medium|high|max]", screen: true},
	{name: "exit", desc: "Exit the CLI", aliases: []string{"quit"}},
	{name: "export", desc: "Export the current conversation to a file or clipboard", args: "[file]", screen: true},
	{name: "fast", desc: "Toggle fast mode"},
	{name: "feedback", desc: "Send feedback to Anthropic or report a bug", screen: true},
	{name: "fewer-permission-prompts", desc: "Add an allowlist for common read-only tool calls", prompt: true},
	{name: "focus", desc: "Toggle focus view: just your prompt, summary, and response"},
	{name: "fork", desc: "Copy this conversation into a new background session"},
	{name: "goal", desc: "Set a goal Claude checks before stopping", args: "<condition>"},
	{name: "help", desc: "Show help and available commands", screen: true},
	{name: "hooks", desc: "View hook configurations for tool events", screen: true},
	{name: "ide", desc: "Manage IDE integrations and show status", screen: true},
	{name: "import", desc: "Import config from another AI coding agent", screen: true},
	{name: "init", desc: "Initialize a new CLAUDE.md file with codebase documentation", prompt: true},
	{name: "insights", desc: "Generate a report analyzing your Claude Code sessions", prompt: true},
	{name: "install-github-app", desc: "Set up Claude GitHub Actions for a repository", screen: true},
	{name: "install-slack-app", desc: "Install the Claude Slack app"},
	{name: "keybindings", desc: "Open your keyboard shortcuts file"},
	{name: "list-agents", desc: "List subagents, teammates, and other Claude sessions you can message"},
	{name: "login", desc: "Sign in with your Anthropic account", screen: true},
	{name: "logout", desc: "Sign out from your Anthropic account"},
	{name: "loop", desc: "Run a prompt or slash command on a recurring interval", args: "[interval] <prompt>", prompt: true},
	{name: "mcp", desc: "Manage MCP servers", screen: true},
	{name: "memory", desc: "Edit CLAUDE.md files and memory settings", screen: true},
	{name: "mobile", desc: "Show QR code to download the Claude mobile app", screen: true},
	{name: "model", desc: "Set the AI model for Claude Code", args: "[model]", screen: true},
	{name: "output-style", desc: "List output styles or switch to one", args: "[style]", screen: true},
	{name: "permissions", desc: "Manage allow and deny tool permission rules", aliases: []string{"allowed-tools"}, screen: true},
	{name: "plan", desc: "Enable plan mode or view the current session plan"},
	{name: "plugin", desc: "Manage Claude Code plugins", screen: true},
	{name: "powerup", desc: "Discover Claude Code features through quick interactive lessons", screen: true},
	{name: "privacy-settings", desc: "View and update your privacy settings", screen: true},
	{name: "rate-limit-options", desc: "Manage usage limits and upgrade options", screen: true},
	{name: "recap", desc: "Generate a one-line session recap now"},
	{name: "release-notes", desc: "View release notes"},
	{name: "reload-plugins", desc: "Activate pending plugin changes in the current session"},
	{name: "reload-skills", desc: "Pick up skills added or changed on disk during this session"},
	{name: "remote-control", desc: "Control this session from your phone or claude.ai/code", aliases: []string{"rc"}, screen: true},
	{name: "remote-env", desc: "Choose the default environment for cloud agents", screen: true},
	{name: "rename", desc: "Rename the current conversation", args: "[name]"},
	{name: "resume", desc: "Resume a previous conversation", args: "[conversation]", aliases: []string{"continue"}, screen: true},
	{name: "rewind", desc: "Restore the code and/or conversation to a previous point", aliases: []string{"checkpoint"}, screen: true},
	{name: "run", desc: "Launch and drive this project's app to see a change working", prompt: true},
	{name: "sandbox", desc: "Configure the sandbox", screen: true},
	{name: "schedule", desc: "Create, update, list, or run scheduled cloud agents", prompt: true},
	{name: "security-review", desc: "Complete a security review of the pending changes on the current branch", prompt: true},
	{name: "simplify", desc: "Review the changed code for reuse, simplification and efficiency, then fix it", prompt: true},
	{name: "skills", desc: "List available skills", screen: true},
	{name: "status", desc: "Show version, model, account, API connectivity, and tool statuses", screen: true},
	{name: "statusline", desc: "Set up Claude Code's status line UI", prompt: true},
	{name: "tasks", desc: "View and manage everything running in the background", aliases: []string{"bashes"}, screen: true},
	{name: "team-onboarding", desc: "Help teammates ramp on Claude Code with a guide from your usage", prompt: true},
	{name: "terminal-setup", desc: "Install Shift+Enter key binding for newlines"},
	{name: "theme", desc: "Change the theme", screen: true},
	{name: "tui", desc: "Set the terminal UI renderer", args: "[default|fullscreen]"},
	{name: "update-config", desc: "Configure the Claude Code harness via settings.json", prompt: true},
	{name: "usage", desc: "Show session cost, plan usage, and activity stats", aliases: []string{"cost", "stats"}, screen: true},
	{name: "verify", desc: "Verify that a code change does what it should, end to end", prompt: true},
	{name: "voice", desc: "Toggle voice mode"},
}

// codexBuiltins is Codex's own commands, as codex-cli 0.153.2 lists them,
// captured the same way ("/" in a throwaway tmux, Down through the menu).
var codexBuiltins = []builtin{
	{name: "agents", desc: "View and switch between all active agent sessions", screen: true},
	{name: "app", desc: "Continue this session in the Desktop app"},
	{name: "approve", desc: "Approve one retry of a recent auto-review denial"},
	{name: "archive", desc: "Archive this session and exit"},
	{name: "cd", desc: "Change the current working directory", args: "<path>"},
	{name: "clear", desc: "Clear the terminal and start a new chat"},
	{name: "compact", desc: "Summarize conversation to prevent hitting the context limit"},
	{name: "copy", desc: "Copy the last response, code block, or quote"},
	{name: "delete", desc: "Permanently delete this session and exit"},
	{name: "diff", desc: "Show git diff (including untracked files)"},
	{name: "exit", desc: "Exit Codex", aliases: []string{"quit"}},
	{name: "experimental", desc: "Toggle experimental features", screen: true},
	{name: "export", desc: "Export the conversation as markdown"},
	{name: "fast", desc: "2x speed, increased usage"},
	{name: "feedback", desc: "Send logs to maintainers", screen: true},
	{name: "fork", desc: "Fork the current chat"},
	{name: "goal", desc: "Set or view the goal for a long-running task", args: "[goal]"},
	{name: "hooks", desc: "View and manage lifecycle hooks", screen: true},
	{name: "ide", desc: "Include current selection, open files, and other context from your IDE"},
	{name: "import", desc: "Import setup, this project, and recent chats from Claude Code", screen: true},
	{name: "init", desc: "Create an AGENTS.md file with instructions for Codex", prompt: true},
	{name: "keymap", desc: "Remap TUI shortcuts", screen: true},
	{name: "logout", desc: "Log out of Codex"},
	{name: "mcp", desc: "List configured MCP tools", args: "[verbose]"},
	{name: "memories", desc: "Configure memory use and generation", screen: true},
	{name: "mention", desc: "Mention a file", screen: true},
	{name: "model", desc: "Choose what model and reasoning effort to use", screen: true},
	{name: "new", desc: "Start a new chat during a conversation"},
	{name: "permissions", desc: "Choose what Codex is allowed to do", screen: true},
	{name: "personality", desc: "Choose a communication style for Codex", screen: true},
	{name: "pets", desc: "Choose or hide the terminal pet", screen: true},
	{name: "plan", desc: "Switch to Plan mode"},
	{name: "plugins", desc: "Browse plugins", screen: true},
	{name: "ps", desc: "List background terminals"},
	{name: "pwd", desc: "Show the current working directory"},
	{name: "raw", desc: "Toggle raw scrollback mode for copy-friendly terminal selection"},
	{name: "recap", desc: "Summarize the current conversation now"},
	{name: "rename", desc: "Rename the current thread", args: "[name]"},
	{name: "resume", desc: "Resume a saved chat", screen: true},
	{name: "review", desc: "Review my current changes and find issues", prompt: true, screen: true},
	{name: "side", desc: "Start a side conversation in an ephemeral fork"},
	{name: "skills", desc: "Use skills to improve how Codex performs specific tasks", screen: true},
	{name: "status", desc: "Show current session configuration and token usage"},
	{name: "statusline", desc: "Configure which items appear in the status line", screen: true},
	{name: "stop", desc: "Stop all background terminals"},
	{name: "subagents", desc: "Switch between this session's subagents", screen: true},
	{name: "theme", desc: "Choose a syntax highlighting theme", screen: true},
	{name: "title", desc: "Configure which items appear in the terminal title", screen: true},
	{name: "usage", desc: "View account usage or use a usage limit reset", screen: true},
	{name: "vim", desc: "Toggle Vim mode for the composer"},
}

// builtinVersions is the agent version each table was captured from.
var builtinVersions = map[string]string{"claude": "2.1.289", "codex": "0.153.2"}

// agentPrefixes is what each agent reads at the start of a prompt, checked
// the same way. Claude Code 2.1 no longer saves "#" lines to memory: they
// go to the model as a prompt.
var agentPrefixes = map[string]map[string]string{
	"claude": {"!": "Runs in the shell", "#": "Sent as a prompt: Claude Code 2.1 has no # memory (use /memory)"},
	"codex":  {"!": "Runs in the shell"},
}

func builtinsFor(agent string) []builtin {
	switch agent {
	case "claude":
		return claudeBuiltins
	case "codex":
		return codexBuiltins
	}
	return nil
}

// commandName is the command a prompt starts with ("/model" in "/model
// opus"), or "" when it doesn't start with one. A path ("/etc/hosts is
// wrong") is not a command.
func commandName(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.Contains(text[:min(len(text), 2)], "//") {
		return ""
	}
	name := text[1:]
	if i := strings.IndexAny(name, " \t\n"); i >= 0 {
		name = name[:i]
	}
	if name == "" || strings.Contains(name, "/") {
		return ""
	}
	return name
}

// localCommand is the agent's own command a prompt runs, when it is one
// handled by the agent's program rather than sent to its model: such a send
// starts no turn.
func localCommand(agent, text string) (string, bool) {
	// A shell command ("!ls") runs in the agent's program too: no hook says
	// a turn started, even when the agent then answers it.
	if t := strings.TrimSpace(text); strings.HasPrefix(t, "!") && len(t) > 1 && agentPrefixes[agent]["!"] != "" {
		return "!", true
	}
	name := commandName(text)
	if name == "" {
		return "", false
	}
	for _, c := range builtinsFor(agent) {
		if c.name == name || containsStr(c.aliases, name) {
			return "/" + c.name, !c.prompt
		}
	}
	return "", false
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sessionAgent is the agent a session runs: its preset, else what the list
// knows, else its command's first word.
func sessionAgent(sess Session) string {
	if sess.Service != "" {
		return ""
	}
	return firstNonEmpty(sess.Preset, firstNonEmpty(sess.Agent, agentOf(sess.Command)))
}

type commandsEntry struct {
	at  time.Time
	key string
	cat CommandCatalog
}

var commandsCache sync.Map // session name → commandsEntry

func (b *Box) listCommands(w http.ResponseWriter, r *http.Request) error {
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	agent := sessionAgent(sess)
	if agent == "opencode" {
		ep, owner, err := b.openCodeRuntime(r.Context(), sess)
		if err != nil {
			return err
		}
		catalog, err := openCodeCatalogAt(r.Context(), ep, owner.Directory)
		if err != nil {
			return err
		}
		cat := CommandCatalog{Agent: agent, Version: ep.Version, Commands: []Command{}}
		for _, c := range catalog.Commands {
			cat.Commands = append(cat.Commands, Command{Name: "/" + c.Name, Description: c.Description, Kind: "custom", Args: "arguments"})
		}
		writeJSON(w, cat)
		return nil
	}
	key := agent + "\x00" + sess.Dir
	if v, ok := commandsCache.Load(sess.Name); ok {
		if e := v.(commandsEntry); e.key == key && time.Since(e.at) < commandsTTL && r.URL.Query().Get("fresh") == "" {
			writeJSON(w, e.cat)
			return nil
		}
	}
	home, _ := os.UserHomeDir()
	cat := Catalog(agent, home, sess.Dir)
	commandsCache.Store(sess.Name, commandsEntry{at: time.Now(), key: key, cat: cat})
	writeJSON(w, cat)
	return nil
}

// Catalog is what agent takes after a "/" in dir, for the user whose home
// is home.
func Catalog(agent, home, dir string) CommandCatalog {
	cat := CommandCatalog{Agent: agent, Version: builtinVersions[agent], Commands: []Command{}, Prefixes: agentPrefixes[agent]}
	for _, c := range builtinsFor(agent) {
		var aliases []string
		for _, a := range c.aliases {
			aliases = append(aliases, "/"+a)
		}
		cat.Commands = append(cat.Commands, Command{Name: "/" + c.name, Description: c.desc, Kind: "builtin", Args: c.args, Aliases: aliases, Local: !c.prompt, Screen: c.screen})
	}
	switch agent {
	case "claude":
		claudeHome := firstNonEmpty(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude"))
		if dir != "" {
			cat.Commands = append(cat.Commands, mdCommands(filepath.Join(dir, ".claude", "commands"), "", "custom", "project")...)
			cat.Commands = append(cat.Commands, skillCommands(filepath.Join(dir, ".claude", "skills"), "", "project")...)
		}
		cat.Commands = append(cat.Commands, mdCommands(filepath.Join(claudeHome, "commands"), "", "custom", "user")...)
		cat.Commands = append(cat.Commands, skillCommands(filepath.Join(claudeHome, "skills"), "", "user")...)
		cat.Commands = append(cat.Commands, claudePlugins(claudeHome)...)
		cat.Notes = append(cat.Notes, "MCP servers' prompts are not listed: the agent reads them from each server once it starts, and berth doesn't start them.")
	case "codex":
		codexHome := firstNonEmpty(os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
		for _, c := range mdCommands(filepath.Join(codexHome, "prompts"), "", "custom", "user") {
			c.Name = "/prompts:" + strings.TrimPrefix(c.Name, "/")
			cat.Commands = append(cat.Commands, c)
		}
	}
	// The first of a name wins: a project's command over the person's.
	seen := map[string]bool{}
	out := cat.Commands[:0]
	for _, c := range cat.Commands {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		c.Description = clipDesc(c.Description)
		out = append(out, c)
	}
	if len(out) > maxCommands {
		out, cat.Truncated = out[:maxCommands], true
	}
	cat.Commands = out
	return cat
}

func clipDesc(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxCommandDesc {
		return s
	}
	cut := maxCommandDesc
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

// mdCommands reads custom commands: every .md under root, named by its
// path ("frontend/test.md" is "/frontend:test"), prefixed with ns.
func mdCommands(root, ns, kind, source string) []Command {
	var out []Command
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		// Claude Code leaves out files named _like-this (a plugin's notes).
		if n++; n > maxCommands || !strings.HasSuffix(d.Name(), ".md") || strings.HasPrefix(d.Name(), "_") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		name := strings.ReplaceAll(strings.TrimSuffix(rel, ".md"), string(filepath.Separator), ":")
		fm, body := readFrontmatter(p)
		desc := firstNonEmpty(fm["description"], firstLine(body))
		out = append(out, Command{Name: "/" + ns + name, Description: desc, Kind: kind, Args: fm["argument-hint"], Source: source})
		return nil
	})
	return out
}

// skillCommands reads skills: root/*/SKILL.md, named by their frontmatter.
func skillCommands(root, ns, source string) []Command {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Command
	for _, e := range entries {
		if len(out) >= maxCommands {
			break
		}
		if !e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		path := filepath.Join(root, e.Name(), "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		fm, body := readFrontmatter(path)
		if fm["user-invocable"] == "false" {
			continue
		}
		name := firstNonEmpty(fm["name"], e.Name())
		kind := "skill"
		if source != "user" && source != "project" {
			kind = "plugin"
		}
		out = append(out, Command{Name: "/" + ns + name, Description: firstNonEmpty(fm["description"], firstLine(body)), Kind: kind, Args: fm["argument-hint"], Source: source})
	}
	return out
}

// claudePlugins reads the enabled plugins' commands and skills, named
// "/plugin:name".
func claudePlugins(claudeHome string) []Command {
	var installed struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	b, err := os.ReadFile(filepath.Join(claudeHome, "plugins", "installed_plugins.json"))
	if err != nil || json.Unmarshal(b, &installed) != nil {
		return nil
	}
	var settings struct {
		Enabled map[string]bool `json:"enabledPlugins"`
	}
	if b, err := os.ReadFile(filepath.Join(claudeHome, "settings.json")); err == nil {
		_ = json.Unmarshal(b, &settings)
	}
	ids := make([]string, 0, len(installed.Plugins))
	for id := range installed.Plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []Command
	for _, id := range ids {
		if on, ok := settings.Enabled[id]; ok && !on {
			continue
		}
		rows := installed.Plugins[id]
		if len(rows) == 0 || rows[len(rows)-1].InstallPath == "" {
			continue
		}
		dir := rows[len(rows)-1].InstallPath
		plugin, _, _ := strings.Cut(id, "@")
		for _, c := range mdCommands(filepath.Join(dir, "commands"), plugin+":", "plugin", plugin) {
			out = append(out, c)
		}
		out = append(out, skillCommands(filepath.Join(dir, "skills"), plugin+":", plugin)...)
	}
	return out
}

// readFrontmatter reads a Markdown file's YAML frontmatter as flat string
// keys (a folded or literal block joined into one line), and the first
// lines of its body. It reads at most 32 KB.
func readFrontmatter(path string) (map[string]string, string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ""
	}
	defer f.Close()
	buf := make([]byte, 32<<10)
	n, _ := f.Read(buf)
	return parseFrontmatter(buf[:n])
}

func parseFrontmatter(b []byte) (map[string]string, string) {
	fm := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<10)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return fm, string(b)
	}
	var key string
	var block []string
	flush := func() {
		if key != "" && block != nil {
			fm[key] = strings.TrimSpace(strings.Join(block, " "))
		}
		key, block = "", nil
	}
	rest := 0
	for sc.Scan() {
		line := sc.Text()
		rest += len(line) + 1
		if strings.TrimSpace(line) == "---" {
			flush()
			break
		}
		if block != nil && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.TrimSpace(line) == "") {
			block = append(block, strings.TrimSpace(line))
			continue
		}
		flush()
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch v {
		case ">", "|", ">-", "|-", ">+", "|+":
			key, block = k, []string{}
		default:
			fm[k] = unquote(v)
		}
	}
	var body strings.Builder
	for sc.Scan() {
		body.WriteString(sc.Text())
		body.WriteByte('\n')
		if body.Len() > 4<<10 {
			break
		}
	}
	return fm, body.String()
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		if v[0] == '"' {
			if s, err := strconv.Unquote(v); err == nil {
				return s
			}
		}
		return v[1 : len(v)-1]
	}
	return v
}

// firstLine is a body's first line of words, without Markdown's heading
// marks.
func firstLine(body string) string {
	for _, l := range strings.Split(body, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")); l != "" {
			return l
		}
	}
	return ""
}

// ---- Files for "@" --------------------------------------------------------

// FileList is the worktree's files matching a query, for "@" mentions.
type FileList struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated,omitempty"`
}

const (
	maxListedFiles = 20000
	maxFileResults = 50
)

type filesEntry struct {
	at    time.Time
	dir   string
	files []string
	more  bool
}

func (b *Box) listFiles(w http.ResponseWriter, r *http.Request) error {
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	// The same list the worktree's ⌘P reads, cached by folder.
	all, more, err := cachedFiles(r.Context(), sess.Dir)
	if err != nil {
		return err
	}
	limit := maxFileResults
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n < limit {
		limit = n
	}
	files := MatchFiles(all, r.URL.Query().Get("q"), limit)
	writeJSON(w, FileList{Files: files, Truncated: more})
	return nil
}

// gitFiles lists the files git tracks in dir and the untracked ones it
// doesn't ignore, up to maxListedFiles.
func gitFiles(ctx context.Context, dir string) ([]string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := gitOut(ctx, dir, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, false, badRequest("git can't list the files here: %v", err)
	}
	var files []string
	seen := map[string]bool{}
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) == 0 || seen[string(f)] {
			continue
		}
		if len(files) >= maxListedFiles {
			return files, true, nil
		}
		seen[string(f)] = true
		files = append(files, string(f))
	}
	return files, false, nil
}

// MatchFiles is the files that match q, best first: a name that starts with
// it, then one containing it, then a path containing it, then a path with
// its letters in order. An empty q is the first files.
func MatchFiles(all []string, q string, limit int) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return append([]string{}, all[:min(limit, len(all))]...)
	}
	type hit struct {
		f     string
		score int
	}
	var hits []hit
	for _, f := range all {
		lf := strings.ToLower(f)
		base := lf[strings.LastIndexByte(lf, '/')+1:]
		s := 0
		switch {
		case strings.HasPrefix(base, q):
			s = 4
		case strings.Contains(base, q):
			s = 3
		case strings.Contains(lf, q):
			s = 2
		case subsequence(lf, q):
			s = 1
		}
		if s > 0 {
			hits = append(hits, hit{f, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return len(hits[i].f) < len(hits[j].f)
	})
	out := make([]string, 0, min(limit, len(hits)))
	for _, h := range hits[:min(limit, len(hits))] {
		out = append(out, h.f)
	}
	return out
}

func subsequence(s, q string) bool {
	i := 0
	for j := 0; j < len(s) && i < len(q); j++ {
		if s[j] == q[i] {
			i++
		}
	}
	return i == len(q)
}
