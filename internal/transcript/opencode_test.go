package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestOpenCodeConversationAndToolDetails(t *testing.T) {
	b, err := os.ReadFile("testdata/opencode.json")
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Data []OpenCodeMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		t.Fatal(err)
	}
	r := OpenCode("ses_acme", "/acme", page.Data, false)
	if r.Source != "opencode" || !r.Reset || r.File != "ses_acme" || r.Next != 6 || r.Items[0].Text != "Fix the retry test" || r.Items[3].Text != "I found the retry bug." {
		t.Fatalf("conversation = %+v", r)
	}
	if !r.Items[1].Done || r.Items[4].Done || r.Items[1].Items[0].Target != "src/retry.ts" {
		t.Fatalf("tool state = %+v", r.Items)
	}
	feed, _ := json.Marshal(r)
	for _, private := range []string{"private reasoning", "export function", "expected retry count", "await retry()"} {
		if strings.Contains(string(feed), private) {
			t.Errorf("feed leaked %q", private)
		}
	}
	d, err := OpenCodeDetail(page.Data[1], "/acme", "msg_acme_tools:call_test")
	if err != nil || !d.Error || d.Pending || d.Command != "pnpm test" || d.Output != "expected retry count: 2\none test failed" {
		t.Fatalf("tool detail = %+v, %v", d, err)
	}
	d, err = OpenCodeDetail(page.Data[0], "/acme", "msg_acme_reply:call_edit")
	if err != nil || !d.Pending || d.File != "src/retry.ts" || d.Old != "retry()" || d.New != "await retry()" {
		t.Fatalf("edit = %+v, %v", d, err)
	}
	if _, err := OpenCodeDetail(page.Data[0], "/acme", "msg_other:call_edit"); !errors.Is(err, ErrNoTool) {
		t.Fatal(err)
	}
	// The same IDs replace a streaming response, rather than duplicating it.
	page.Data[0].Content[1].Text += " Fixed it."
	page.Data[0].Content[2].State.Status = "completed"
	next := OpenCode("ses_acme", "/acme", page.Data, true)
	if !next.More || next.Items[3].ID != r.Items[3].ID || !next.Items[4].Done || next.Items[3].Text != "I found the retry bug. Fixed it." {
		t.Fatalf("update = %+v", next)
	}
}
