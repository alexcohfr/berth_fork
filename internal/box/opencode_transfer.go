package box

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type openCodeTransferFile struct {
	Executable bool   `json:"executable,omitempty"`
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	Before     string `json:"before,omitempty"`
	Etag       string `json:"etag"`
}
type openCodeTransferRoot struct {
	Directory string
	Global    bool
}

func openCodeTransferPath(name string) bool {
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		return false
	}
	if name == "AGENTS.md" {
		return true
	}
	if !strings.HasPrefix(name, ".opencode/agents/") && !strings.HasPrefix(name, ".opencode/commands/") && !strings.HasPrefix(name, ".opencode/skills/") {
		return false
	}
	for _, part := range strings.Split(name, "/")[1:] {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".txt", ".sh", ".py", ".js", ".ts", ".json", ".jsonc", ".yaml", ".yml":
		return true
	}
	return false
}

// Templates with inline credentials are not portable config. Credentials are
// reconnected using OpenCode on the destination, never added to this bundle.
var openCodeInlineSecret = regexp.MustCompile(`(?im)(api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|client[_-]?secret|password)["']?\s*[:=]\s*["']?[^\s"'}]+`)
var openCodeRelativeReference = regexp.MustCompile(`\{file:([^}]+)\}|\]\(([^)\s]+)\)`)

func openCodeTransferFilePath(root openCodeTransferRoot, name string) (string, error) {
	if !openCodeTransferPath(name) {
		return "", badRequest("select only rules, agents, commands and skill files")
	}
	if root.Global {
		name = strings.TrimPrefix(name, ".opencode/")
	}
	abs := filepath.Join(root.Directory, filepath.FromSlash(name))
	current := root.Directory
	for _, part := range strings.Split(name, "/") {
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", badRequest("configuration transfers cannot follow symlinks")
		}
	}
	return abs, nil
}

func openCodeTransferRead(root openCodeTransferRoot, name string) (openCodeTransferFile, error) {
	f := openCodeTransferFile{Path: name, Etag: "missing"}
	abs, err := openCodeTransferFilePath(root, name)
	if err != nil {
		return f, err
	}
	st, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if !st.Mode().IsRegular() || st.Size() > 128<<10 {
		return f, badRequest("configuration file must be text smaller than 128 KB")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return f, err
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) || openCodeInlineSecret.Match(data) {
		return f, badRequest("file is not portable text or contains an inline credential; reconnect on the destination")
	}
	f.Executable = st.Mode().Perm()&0o111 != 0
	f.Content, f.Etag = string(data), etagOf(append([]byte{byte(st.Mode().Perm() & 0o111)}, data...))
	return f, nil
}

func openCodeTransferDependencies(root openCodeTransferRoot, files []openCodeTransferFile) error {
	selected := map[string]bool{}
	for _, f := range files {
		selected[f.Path] = true
	}
	for _, f := range files {
		for _, m := range openCodeRelativeReference.FindAllStringSubmatch(f.Content, -1) {
			ref := firstNonEmpty(m[1], m[2])
			if strings.Contains(ref, "://") || strings.HasPrefix(ref, "#") {
				continue
			}
			if strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "~") {
				return badRequest("adapt absolute reference in %s before transfer", f.Path)
			}
			ref, _, _ = strings.Cut(ref, "#")
			ref = path.Clean(path.Join(path.Dir(f.Path), ref))
			if selected[ref] {
				continue
			}
			if _, err := openCodeTransferFilePath(root, ref); err != nil {
				return badRequest("invalid dependency in %s", f.Path)
			}
			got, err := openCodeTransferRead(root, ref)
			if err != nil || got.Etag == "missing" {
				return badRequest("include missing dependency %s before activation", ref)
			}
		}
	}
	return nil
}

func (b *Box) openCodeTransfer(w http.ResponseWriter, r *http.Request) error {
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	ep, owner, err := b.openCodeRuntime(r.Context(), sess)
	if err != nil {
		return err
	}
	root := openCodeTransferRoot{Directory: owner.Directory}
	scope := r.URL.Query().Get("scope")
	if scope == "user" {
		home := b.Sessions.EnvVar(r.Context(), sess, "HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		config := b.Sessions.EnvVar(r.Context(), sess, "OPENCODE_CONFIG_DIR")
		if config == "" {
			xdg := b.Sessions.EnvVar(r.Context(), sess, "XDG_CONFIG_HOME")
			if xdg == "" {
				xdg = filepath.Join(home, ".config")
			}
			config = filepath.Join(xdg, "opencode")
		}
		canonical, err := filepath.EvalSymlinks(config)
		if err != nil {
			return err
		}
		root = openCodeTransferRoot{Directory: canonical, Global: true}
	} else if scope != "" && scope != "project" {
		return badRequest("scope must be user or project")
	}
	if r.Method == "GET" {
		out := []openCodeTransferFile{}
		for _, sub := range []string{"AGENTS.md", ".opencode/agents", ".opencode/commands", ".opencode/skills"} {
			physical := sub
			if root.Global {
				physical = strings.TrimPrefix(sub, ".opencode/")
			}
			err := filepath.WalkDir(filepath.Join(root.Directory, physical), func(abs string, d fs.DirEntry, err error) error {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				if err != nil {
					return err
				}
				if d.Type()&os.ModeSymlink != 0 {
					return nil
				}
				if d.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(root.Directory, abs)
				rel = filepath.ToSlash(rel)
				if root.Global && rel != "AGENTS.md" {
					rel = ".opencode/" + rel
				}
				if !openCodeTransferPath(rel) {
					return nil
				}
				if len(out) >= 200 {
					return badRequest("transfer catalog exceeds 200 files")
				}
				f, err := openCodeTransferRead(root, rel)
				if err != nil {
					return nil
				}
				f.Content = ""
				out = append(out, f)
				return nil
			})
			if err != nil {
				return err
			}
		}
		writeJSON(w, map[string]any{"files": out, "scope": firstNonEmpty(scope, "project"), "directory": root.Directory})
		return nil
	}
	var req struct {
		Instance string                 `json:"instance"`
		Action   string                 `json:"action"`
		Paths    []string               `json:"paths"`
		Files    []openCodeTransferFile `json:"files"`
	}
	if err := decodeLimit(r, &req, 2<<20); err != nil {
		return err
	}
	if req.Instance != ep.ID {
		return httpError{http.StatusConflict, "OpenCode runtime changed"}
	}
	if len(req.Paths) > 100 || len(req.Files) > 100 {
		return badRequest("select at most 100 files")
	}
	if req.Action == "export" {
		out := []openCodeTransferFile{}
		size := 0
		for _, p := range req.Paths {
			f, err := openCodeTransferRead(root, p)
			if err != nil {
				return err
			}
			if f.Etag == "missing" {
				return badRequest("selected file no longer exists")
			}
			size += len(f.Content)
			if size > 1<<20 {
				return badRequest("transfer exceeds 1 MB")
			}
			out = append(out, f)
		}
		writeJSON(w, map[string]any{"files": out})
		return nil
	}
	if req.Action != "preview" && req.Action != "apply" {
		return badRequest("unsupported configuration transfer action")
	}
	if len(req.Files) == 0 {
		return badRequest("select files to transfer")
	}
	// Deterministic per-file locks share the editor's concurrency guard.
	sort.Slice(req.Files, func(i, j int) bool { return req.Files[i].Path < req.Files[j].Path })
	for i, f := range req.Files {
		if i > 0 && req.Files[i-1].Path == f.Path {
			return badRequest("duplicate destination")
		}
		abs, err := openCodeTransferFilePath(root, f.Path)
		if err != nil {
			return err
		}
		defer lockFile(abs)()
	}
	out := []openCodeTransferFile{}
	size := 0
	for _, f := range req.Files {
		size += len(f.Content)
		if len(f.Content) > 128<<10 || size > 1<<20 || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) || openCodeInlineSecret.MatchString(f.Content) {
			return badRequest("invalid transfer text or inline credentials")
		}
		old, err := openCodeTransferRead(root, f.Path)
		if err != nil {
			return err
		}
		if req.Action == "apply" && f.Etag != old.Etag {
			return httpError{http.StatusPreconditionFailed, "Destination changed since preview; preview again"}
		}
		f.Before, f.Etag = old.Content, old.Etag
		out = append(out, f)
	}
	if err := openCodeTransferDependencies(root, out); err != nil {
		return err
	}
	if req.Action == "apply" {
		if err := b.before(r, "session.send", map[string]any{"name": sess.Name, "path": sess.Dir, "action": "opencode.transfer"}); err != nil {
			return err
		}
		for _, f := range out {
			abs, err := openCodeTransferFilePath(root, f.Path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
				return err
			}
			mode := fs.FileMode(0o600)
			if f.Executable {
				mode = 0o700
			}
			if err := writeAtomic(abs, []byte(f.Content), mode); err != nil {
				return err
			}
		}
	}
	writeJSON(w, map[string]any{"files": out, "applied": req.Action == "apply"})
	return nil
}
