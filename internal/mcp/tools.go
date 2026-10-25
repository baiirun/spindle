package mcp

import (
	"context"
	"encoding/json"

	"spindle/internal/memory"
)

// MemoryTools are the main agent's way into project memory. They replace
// Claude Code's file tools on its memory folder, so the same instructions work
// in Codex, whose sandbox may not reach ~/.spindle. Each tool is a thin
// wrapper over memory.Client, as the CLI's commands are; agent names the
// caller in the activity log.
func MemoryTools(p memory.Project, agent string) []Tool {
	c := p.Client(agent)
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
				m, err := c.Read(a.Name)
				if err != nil {
					return "", err
				}
				return memory.FormatMemory(m), nil
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
				return c.Write(o)
			},
		},
		{
			Name:        "memory_delete",
			Description: "Delete a memory that is wrong, outdated or that the user asked to forget.",
			InputSchema: schema(`{"type":"object","properties":{"name":{"type":"string"},"reason":{"type":"string"}},"required":["name","reason"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct{ Name, Reason string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				return c.Delete(a.Name, a.Reason)
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
				hits, total, err := c.Search(a.Query, a.Limit)
				if err != nil {
					return "", err
				}
				return memory.FormatSearch(hits, total), nil
			},
		},
		{
			Name:        "memory_index",
			Description: "The current memory index (MEMORY.md), in case it changed since the session started.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				if s := c.Index(); s != "" {
					return s, nil
				}
				return "(empty)", nil
			},
		},
		{
			Name:        "memory_status",
			Description: "How many memories the project has by type, when memory was last consolidated, and how far each session has been read.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				s, err := c.Status()
				if err != nil {
					return "", err
				}
				return memory.FormatStatus(s), nil
			},
		},
	}
}

func schema(s string) json.RawMessage {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("bad schema: " + err.Error())
	}
	b, _ := json.Marshal(v)
	return b
}
