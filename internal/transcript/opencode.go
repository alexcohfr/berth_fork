package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OpenCodeMessage is the public V2 Session.Message.Info projection. Read it
// through the API, never OpenCode's private database schema.
type OpenCodeMessage struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	Command string `json:"command"`
	Output  string `json:"output"`
	Time    struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Content []OpenCodePart `json:"content"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type OpenCodePart struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Text  string `json:"text"`
	State struct {
		Status  string          `json:"status"`
		Input   json.RawMessage `json:"input"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"state"`
	Time struct {
		Created int64 `json:"created"`
	} `json:"time"`
}

// OpenCode reads a newest-first API page into a chronological chat window.
// A fresh window also replaces partial text, resolved tools and reverted items.
func OpenCode(id, dir string, messages []OpenCodeMessage, more bool) Result {
	r := Result{Source: "opencode", File: id, Gen: id, Reset: true, Items: []Item{}, Crew: []CrewMember{}, Truncated: more}
	if more {
		r.Items = append(r.Items, Item{Kind: "notice", ID: "opencode:older", Notice: "memory", Level: "info", Text: "Showing the latest 100 OpenCode messages. Earlier messages are available in OpenCode."})
	}
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		r.Last = max(r.Last, m.Time.Created, m.Time.Completed)
		switch m.Type {
		case "user":
			r.Items = append(r.Items, Item{Kind: "user", ID: m.ID, Text: clip(m.Text, maxText)})
		case "assistant":
			for n, p := range m.Content {
				key := fmt.Sprintf("%s:%d", m.ID, n)
				switch p.Type {
				case "text":
					if p.Text != "" {
						r.Items = append(r.Items, Item{Kind: "text", ID: key, Text: clip(p.Text, maxText)})
					}
				case "tool":
					d := openCodeDetail(m.ID, p, dir)
					verb, target, file := openCodeTool(p.Name, d)
					r.Items = append(r.Items, Item{Kind: "tools", ID: key, Verb: verb, Done: !d.Pending,
						Items: []ToolCall{{ID: d.ID, Verb: verb, Target: target, File: file, At: p.Time.Created}}})
				}
			}
			if m.Error != nil {
				r.Items = append(r.Items, Item{Kind: "notice", ID: m.ID + ":error", Notice: "api_error", Level: "error", Text: clip(m.Error.Message, 2000)})
			}
		case "shell":
			r.Items = append(r.Items, Item{Kind: "command", ID: m.ID, Command: "!", Args: m.Command, Text: clip(m.Output, maxText)})
		}
	}
	r.Next = len(r.Items)
	return r
}

// OpenCodeDetail reads only the requested tool; reasoning, other tool output
// and file attachments never enter the normal chat feed.
func OpenCodeDetail(m OpenCodeMessage, dir, id string) (ToolDetail, error) {
	for _, p := range m.Content {
		if p.Type == "tool" && m.ID+":"+p.ID == id {
			return openCodeDetail(m.ID, p, dir), nil
		}
	}
	return ToolDetail{}, ErrNoTool
}

func openCodeDetail(message string, p OpenCodePart, dir string) ToolDetail {
	var in map[string]any
	_ = json.Unmarshal(p.State.Input, &in) // streaming input is still a string
	str := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := in[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	d := ToolDetail{ID: message + ":" + p.ID, Name: p.Name,
		Command: clip(str("command", "code"), textCap), Pattern: clip(str("pattern", "query", "url"), textCap),
		Old: clip(str("oldString", "old_string"), textCap), New: clip(str("newString", "new_string", "content", "patchText"), textCap),
		Pending: p.State.Status != "completed" && p.State.Status != "error", Error: p.State.Status == "error"}
	if path := str("filePath", "file_path", "path"); path != "" {
		d.File = rel(dir, path)
	}
	var output []string
	for _, c := range p.State.Content {
		if c.Type == "text" {
			output = append(output, c.Text)
		}
	}
	if p.State.Error != nil {
		output = append(output, p.State.Error.Message)
	}
	text := strings.Join(output, "\n")
	d.Output, d.Truncated = clip(text, outputCap), len(text) > outputCap
	return d
}

func openCodeTool(name string, d ToolDetail) (verb, target string, file bool) {
	switch name {
	case "read":
		return "Read", d.File, true
	case "edit", "write", "patch":
		return "Edit", firstNonEmpty(d.File, name), d.File != ""
	case "grep", "glob", "websearch":
		return "Search", firstNonEmpty(d.Pattern, d.File), false
	case "shell", "bash", "execute":
		return "Run", clip(d.Command, 300), false
	default:
		return name, clip(firstNonEmpty(d.Pattern, firstNonEmpty(d.File, d.Command)), 300), d.File != ""
	}
}
