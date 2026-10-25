package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"spindle/internal/memory"
)

func TestTools(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	memory.Store(home, "proj").Link(dir)
	c := memory.NewClient(home, dir, "", "claude")
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_write","arguments":{"name":"No Mocks","type":"directive","description":"Integration tests hit a real database","body":"Real DB.\n\n**Why:** mocks hid a broken migration.\n**How to apply:** tests touching storage."}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_search","arguments":{"query":"database tests"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"memory_read","arguments":{"name":"no-mocks"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"memory_index"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"memory_write","arguments":{"name":"x","type":"feedback","description":"d","body":"b"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"memory_delete","arguments":{"name":"no-mocks","reason":"user asked to forget"}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"memory_delete","arguments":{"name":"no-mocks","reason":"again"}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"nope"}`,
	}, "\n")
	var out strings.Builder
	if err := (Server{Name: "spindle", Version: "t", Tools: MemoryTools(c, nil)}).Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	type resp struct {
		ID     int
		Result struct {
			Content         []struct{ Text string }
			IsError         bool
			Tools           []struct{ Name string }
			ProtocolVersion string
		}
		Error *Error
	}
	got := map[int]resp{}
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var r resp
		json.Unmarshal(sc.Bytes(), &r)
		got[r.ID] = r
	}
	text := func(id int) string {
		if len(got[id].Result.Content) == 0 {
			return ""
		}
		return got[id].Result.Content[0].Text
	}
	if len(got) != 10 || got[1].Result.ProtocolVersion != "2025-03-26" || len(got[2].Result.Tools) != 12 {
		t.Fatalf("responses:\n%s", out.String())
	}
	if text(3) != "Saved proj/no-mocks.md." || !strings.Contains(text(4), "no-mocks.md") || !strings.Contains(text(5), "**Why:** mocks") || !strings.Contains(text(6), "[no-mocks](proj/no-mocks.md)") {
		t.Fatalf("write/search/read/index:\n%s", out.String())
	}
	if !got[7].Result.IsError || text(8) != "Deleted proj/no-mocks.md." || !got[9].Result.IsError || got[10].Error == nil {
		t.Fatalf("errors:\n%s", out.String())
	}
	changes, _ := memory.Store(home, "proj").Changes()
	if len(changes) != 2 || changes[0].By != "agent" {
		t.Fatalf("changes = %+v", changes)
	}
}
