package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"spindle/internal/memory"
)

// MemoryTools are the main agent's way into project memory. They replace
// Claude Code's file tools on its memory folder, so the same instructions work
// in Codex, whose sandbox may not reach ~/.spindle.
func MemoryTools(p memory.Project) []Tool {
	return []Tool{
		{
			Name:        "memory_read",
			Description: "Read one memory file in full, by name (as listed in the memory index).",
			InputSchema: schema(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct{ Name string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				m, ok := p.Get(a.Name)
				if !ok {
					return "", fmt.Errorf("no memory %q", memory.NameOf(a.Name))
				}
				return fmt.Sprintf("Memory: %s (modified %s):\n\n%s", m.File(), m.Modified.UTC().Format("2006-01-02"), m.Render()), nil
			},
		},
		{
			Name: "memory_write",
			Description: "Create or replace a memory. Writing an existing name replaces the whole file, so give the full body. " +
				"type: user | directive | project | reference. description: one specific line, used to decide relevance later.",
			InputSchema: schema(`{"type":"object","properties":{
"name":{"type":"string","description":"short kebab-case slug"},
"type":{"type":"string","enum":["user","directive","project","reference"]},
"description":{"type":"string"},
"body":{"type":"string","description":"for directive and project: the rule or fact, then **Why:** and **How to apply:** lines"}},
"required":["name","type","description","body"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var o memory.Op
				if err := json.Unmarshal(raw, &o); err != nil {
					return "", err
				}
				o.Op = "write"
				return apply(p, o)
			},
		},
		{
			Name:        "memory_delete",
			Description: "Delete a memory that is wrong, outdated or that the user asked to forget.",
			InputSchema: schema(`{"type":"object","properties":{"name":{"type":"string"},"reason":{"type":"string"}},"required":["name","reason"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var o memory.Op
				if err := json.Unmarshal(raw, &o); err != nil {
					return "", err
				}
				o.Op = "delete"
				return apply(p, o)
			},
		},
		{
			Name:        "memory_search",
			Description: "Search all memory files (names, descriptions and bodies) for words. Use it for memories the index doesn't list or to check before writing.",
			InputSchema: schema(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Query string
					Limit int
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				mems, err := p.Load()
				if err != nil {
					return "", err
				}
				hits := memory.Search(mems, a.Query, a.Limit)
				var b strings.Builder
				fmt.Fprintf(&b, "%d of %d memories match.\n", len(hits), len(mems))
				for _, m := range hits {
					fmt.Fprintf(&b, "- [%s] %s (%s): %s\n", m.Type, m.File(), m.Modified.UTC().Format("2006-01-02"), m.Description)
				}
				return b.String(), nil
			},
		},
		{
			Name:        "memory_index",
			Description: "The current memory index (MEMORY.md), in case it changed since the session started.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				if s := p.ReadIndex(); s != "" {
					return s, nil
				}
				return "(empty)", nil
			},
		},
	}
}

func apply(p memory.Project, o memory.Op) (string, error) {
	unlock, err := p.Lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	res, err := p.Apply([]memory.Op{o}, "agent", "", time.Now())
	if err != nil {
		return "", err
	}
	name := memory.NameOf(o.Name)
	switch {
	case res.Written > 0:
		return "Saved " + name + ".md.", nil
	case res.Deleted > 0:
		return "Deleted " + name + ".md.", nil
	case o.Op == "delete":
		return "", fmt.Errorf("no memory %q", name)
	case res.Skipped > 0:
		return "", fmt.Errorf("not saved: name, a valid type, description and body are all required")
	}
	return "Unchanged.", nil
}

func schema(s string) json.RawMessage {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("bad schema: " + err.Error())
	}
	b, _ := json.Marshal(v)
	return b
}
