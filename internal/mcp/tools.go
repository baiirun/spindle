package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"spindle/internal/memory"
)

// MemoryTools are the main agent's way into memory. They replace Claude
// Code's file tools on its memory folder, so the same instructions work in
// Codex, whose sandbox may not reach ~/.spindle. Each tool is a thin wrapper
// over memory.Client, as the CLI's commands are; the client's caller names
// the agent in the activity log, and startDream starts a dream of the
// session's project in the background.
func MemoryTools(c memory.Client, startDream func() error) []Tool {
	return []Tool{
		{
			Name:        "memory_read",
			Description: "Read one memory file in full, by name as listed: <project>/<name>, like global/role.",
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
				"type: user | directive | project | reference. description: one specific line, used to decide relevance later. " +
				"It goes to this folder's project unless project says otherwise (type user: global).",
			InputSchema: schema(`{"type":"object","properties":{
"name":{"type":"string","description":"short kebab-case slug"},
"type":{"type":"string","enum":["user","directive","project","reference"]},
"project":{"type":"string","description":"global (who the user is, rules for everywhere), another project the fact is about, or a new short name to create one. Default: this folder's project; global for type user"},
"project_description":{"type":"string","description":"one line on what a new project is about"},
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
			Description: "Search every project's memory files (names, descriptions and bodies) for words. Use it for memories the index doesn't list or to check before writing.",
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
			Description: "The memory index as it is now: this folder's project, global, and a line per other project.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				return c.Index(), nil
			},
		},
		{
			Name:        "memory_status",
			Description: "This folder's project: how many memories by type, when it was last consolidated, its folders; and counts for the rest.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				s, err := c.Status()
				if err != nil {
					return "", err
				}
				return memory.FormatStatus(s), nil
			},
		},
		{
			Name:        "memory_projects",
			Description: "List every project with its description, folders and how many memories it has. * marks this folder's project.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				ps, err := c.Projects()
				if err != nil {
					return "", err
				}
				return memory.FormatProjects(ps), nil
			},
		},
		{
			Name: "memory_project",
			Description: "Create or describe a project, or link this folder to it so sessions here start in it. " +
				"Folders only hint at a project; every project's memories stay visible everywhere.",
			InputSchema: schema(`{"type":"object","properties":{
"name":{"type":"string","description":"short kebab-case project name"},
"description":{"type":"string","description":"one line on what the project is about"},
"link_this_folder":{"type":"boolean","description":"link this session's folder (its repository root) to the project"}},
"required":["name"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Name, Description string
					Link              bool `json:"link_this_folder"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				link := ""
				if a.Link {
					link = "."
				}
				return c.SetProject(a.Name, a.Description, link, "")
			},
		},
		{
			Name: "memory_dream",
			Description: "Start consolidating this folder's project's memory now: merge duplicates, fix what recent sessions contradicted, prune. " +
				"It runs in the background for a few minutes; check memory_log for the result. Only when the user asks.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				if startDream == nil {
					return "", errors.New("dreaming is not available here")
				}
				if err := startDream(); err != nil {
					return "", err
				}
				return "Dream started. It takes a few minutes; memory_log shows when it finishes and what it changed.", nil
			},
		},
		{
			Name: "memory_log",
			Description: "What memory has been doing, across projects: session starts, which memories were recalled for which messages, " +
				"saves, deletes, skipped passes and dreams. For when the user asks about memory itself.",
			InputSchema: schema(`{"type":"object","properties":{
"session":{"type":"string","description":"only this session (a prefix is enough)"},
"kind":{"type":"string","enum":["start","select","recall","skip","call","extract","dream"]},
"project":{"type":"string","description":"only sessions in this project"},
"limit":{"type":"integer","description":"last n events, default 30"}}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Session, Kind, Project string
					Limit                  int
				}
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &a); err != nil {
						return "", err
					}
				}
				if a.Limit <= 0 {
					a.Limit = 30
				}
				evs, err := c.Activity(memory.LogFilter{Session: a.Session, Kind: a.Kind, Project: a.Project, Last: a.Limit})
				if err != nil {
					return "", err
				}
				return memory.FormatEvents(evs), nil
			},
		},
		{
			Name: "memory_history",
			Description: "Every change to one memory, newest first: when, by whom (agent, cli, extract, dream), why, and the text before it. " +
				"Old versions are superseded; never treat them as current. For when the user asks how a memory changed.",
			InputSchema: schema(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct{ Name string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				hist, err := c.History(a.Name)
				if err != nil {
					return "", err
				}
				return memory.FormatHistory(a.Name, hist, true), nil
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
