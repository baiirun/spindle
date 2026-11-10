package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"spindle/internal/memory"
)

// MemoryTools are the main agent's way into memory. Each is a thin wrapper
// over memory.Client, as the CLI's commands are; the client's caller names
// the agent in the activity log. Agents only append: no tool here changes
// rules or the wiki.
func MemoryTools(c memory.Client) []Tool {
	return []Tool{
		{
			Name: "memory_append",
			Description: "Note one thing worth remembering, as it happens: a decision and its reason, a fact the user taught you, " +
				"something that went wrong and why. One line, at most 280 bytes. Don't repeat what the index already shows.",
			InputSchema: schema(`{"type":"object","properties":{
"gist":{"type":"string","description":"one line, at most 280 bytes"},
"scope":{"type":"string","enum":["project","global"],"description":"global: about the user, or true in every project. Default: this folder's project"},
"source":{"type":"string","description":"where it came from, if not this conversation: a doc, a URL, a meeting"}},
"required":["gist"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Gist   string
					Scope  memory.Scope
					Source string
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				return c.Append(a.Gist, a.Scope, a.Source)
			},
		},
		{
			Name:        "memory_search",
			Description: "Search rules, wiki pages and rollups in every project by words. Each hit comes with the log entries behind it.",
			InputSchema: schema(`{"type":"object","properties":{
"query":{"type":"string"},
"layers":{"type":"array","items":{"type":"string","enum":["rules","wiki","rollups","log"]},"description":"default: all"}},
"required":["query"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Query  string
					Layers []memory.SearchLayer
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				return c.Search(a.Query, a.Layers, 10)
			},
		},
		{
			Name:        "memory_zoom",
			Description: "Open an id from the memory index: a week, day or session (w:2026-W45, d:2026-11-08, s:...) into its parts, or an entry (e-...) into its gist and where it was said.",
			InputSchema: schema(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct{ ID string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				return c.Zoom(a.ID)
			},
		},
		{
			Name:        "wiki_read",
			Description: "Read one wiki page (a person, project, link, tool, concept or org) with the log entries it rests on.",
			InputSchema: schema(`{"type":"object","properties":{"slug":{"type":"string"}},"required":["slug"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct{ Slug string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				return c.WikiRead(a.Slug)
			},
		},
		{
			Name:        "rules_list",
			Description: "The rules a session here loads, in full: global's and this folder's project's.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				return c.RulesList()
			},
		},
		{
			Name:        "rollup_read",
			Description: "The summary of a session, day or week, with its parts.",
			InputSchema: schema(`{"type":"object","properties":{
"window":{"type":"string","enum":["session","day","week"]},
"at":{"type":"string","description":"a date or RFC 3339 time inside the window; default: now"}},
"required":["window"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct{ Window, At string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				kind, at, err := parseRollupArgs(a.Window, a.At)
				if err != nil {
					return "", err
				}
				return c.RollupRead(kind, at)
			},
		},
		{
			Name:        "memory_projects",
			Description: "List every project with its description, folders and how many entries, rules and pages it has. * marks this folder's project.",
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
				"Linking adopts what was noted here before, while the folder had no project. Every project stays searchable from everywhere.",
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
	}
}

// parseRollupArgs reads rollup_read's window name and time.
func parseRollupArgs(window, at string) (memory.WindowKind, time.Time, error) {
	kind, ok := map[string]memory.WindowKind{"session": memory.SessionWindow, "day": memory.DayWindow, "week": memory.WeekWindow}[window]
	if !ok {
		return "", time.Time{}, fmt.Errorf("window %q: want session, day or week", window)
	}
	if at == "" {
		return kind, time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return kind, t, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, at, time.Local); err == nil {
		return kind, t, nil
	}
	return "", time.Time{}, fmt.Errorf("at %q: want a date (2026-11-08) or an RFC 3339 time", at)
}

func schema(s string) json.RawMessage {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("bad schema: " + err.Error())
	}
	b, _ := json.Marshal(v)
	return b
}
