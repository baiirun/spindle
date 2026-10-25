package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindle/internal/memory"
)

func TestServeMemoryTools(t *testing.T) {
	t.Setenv(memory.ProjectEnv, "")
	home, root := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	p := memory.ProjectAt(home, root)
	s := Server{Name: "spindle", Version: "test", Tools: MemoryTools(p, filepath.Join(home, "corpus"))}

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_write","arguments":{"kind":"decision","key":"ledger budget","text":"No budget: the record is searched, not shown.","who":"user approved","user_words":"agree"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_write","arguments":{"kind":"decision","key":"ledger budget","text":"No budget, and no eviction."}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"ledger budget eviction"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"memory_history","arguments":{"key":"ledger budget"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"memory_drop","arguments":{"key":"nope","reason":"x"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"memory_now"}}`,
	}, "\n") + "\n"
	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	type resp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	got := map[int]resp{}
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var r resp
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		got[r.ID] = r
	}
	if len(got) != 10 {
		t.Fatalf("%d responses, want 10 (no reply to the notification):\n%s", len(got), out.String())
	}
	text := func(id int) (string, bool) {
		var r struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		}
		json.Unmarshal(got[id].Result, &r)
		if len(r.Content) == 0 {
			return "", r.IsError
		}
		return r.Content[0].Text, r.IsError
	}
	if !strings.Contains(string(got[1].Result), `"protocolVersion":"2025-03-26"`) {
		t.Fatalf("initialize = %s", got[1].Result)
	}
	for _, name := range []string{"memory_search", "memory_write", "memory_now", "transcript_search", "transcript_read", "memory_history", "memory_drop"} {
		if !strings.Contains(string(got[2].Result), `"name":"`+name+`"`) {
			t.Fatalf("tools/list lacks %s", name)
		}
	}
	if s, _ := text(4); !strings.HasPrefix(s, "Replaced ledger budget") {
		t.Fatalf("second write = %q", s)
	}
	if s, _ := text(5); !strings.Contains(s, "no eviction") || !strings.Contains(s, "Replaced:") {
		t.Fatalf("search = %q", s)
	}
	if s, _ := text(6); !strings.Contains(s, "replace") || !strings.Contains(s, "searched, not shown") {
		t.Fatalf("history = %q", s)
	}
	if _, isErr := text(7); !isErr {
		t.Fatal("dropping a missing key should be a tool error")
	}
	if got[8].Error == nil || got[9].Error == nil || got[9].Error.Code != -32601 {
		t.Fatalf("unknown tool / method = %+v / %+v", got[8].Error, got[9].Error)
	}
	if s, _ := text(10); !strings.Contains(s, "No Now summary") {
		t.Fatalf("memory_now = %q", s)
	}
}
