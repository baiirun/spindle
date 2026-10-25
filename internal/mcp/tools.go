package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"spindle/internal/memory"
	"spindle/internal/wake"
)

// MemoryTools are the tools for one project: search and read the decision
// record, the Now summary, the user's past messages and the transcripts, and
// write to the record.
func MemoryTools(p memory.Project, corpusRoot string) []Tool {
	return []Tool{
		{
			Name: "memory_search",
			Description: "Search this project's memory: the decision record (decisions in force with their reasons and what they replaced, " +
				"things tried or rejected, the user's standing preferences) and the user's past messages, verbatim. Use it before relying on " +
				"an earlier decision, or when the user refers to something from an earlier session.",
			InputSchema: schema(`{"type":"object","properties":{"query":{"type":"string","description":"words to search for"},"limit":{"type":"integer","description":"max results per kind, default 8"}},"required":["query"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if a.Limit <= 0 {
					a.Limit = 8
				}
				rec, err := p.LoadRecord()
				if err != nil {
					return "", err
				}
				users, err := p.UserMessages()
				if err != nil {
					return "", err
				}
				var b strings.Builder
				hits := rec.Search(a.Query, a.Limit)
				fmt.Fprintf(&b, "Decision record (%d entries, as of %s): %d match(es)\n", len(rec.Entries), asOf(rec.AsOf), len(hits))
				for _, h := range hits {
					fmt.Fprintf(&b, "- [%s] %s\n", h.Entry.Kind, memory.RenderEntry(h.Key, h.Entry))
				}
				ms := memory.SearchUserMessages(users, a.Query, "", a.Limit, false)
				fmt.Fprintf(&b, "\nUser messages (%d kept): %d match(es)\n", len(users), len(ms))
				for _, m := range ms {
					fmt.Fprintf(&b, "- [%s %s %s session %s] %s\n", m.ID, m.Time.UTC().Format("2006-01-02 15:04Z"), m.Agent, short(m.Session), m.Text)
				}
				return b.String(), nil
			},
		},
		{
			Name:        "memory_now",
			Description: "The project's current Now summary: where the work stood, the next step, open questions, and the latest exchange verbatim.",
			InputSchema: schema(`{"type":"object","properties":{}}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				now, err := p.Now()
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(now) == "" {
					return "No Now summary yet for " + p.Name() + ".", nil
				}
				return now, nil
			},
		},
		{
			Name: "memory_write",
			Description: "Add or change an entry in the project's decision record. Use it when the user makes or approves a decision, rejects " +
				"an approach, or states a standing preference that should outlive this session. Reusing an existing key replaces that entry " +
				"and keeps the old text as 'replaced'. Never record your own proposal as the user's decision.",
			InputSchema: schema(`{"type":"object","properties":{
"kind":{"type":"string","enum":["decision","tried","preference"]},
"key":{"type":"string","description":"short, specific, searchable topic name; reuse an existing key to replace it"},
"text":{"type":"string","description":"the decision stated fully enough to apply without the conversation"},
"why":{"type":"string","description":"the reason, in the user's reasoning where possible"},
"who":{"type":"string","description":"user | user approved | assistant"},
"user_words":{"type":"string","description":"the user's words, verbatim, when they stated or approved it"}},
"required":["kind","key","text"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var o memory.Op
				if err := json.Unmarshal(raw, &o); err != nil {
					return "", err
				}
				o.Op = "add"
				if o.Who == "" {
					o.Who = "assistant"
				}
				return writeRecord(p, o)
			},
		},
		{
			Name:        "memory_drop",
			Description: "Remove a record entry that no longer holds. Prefer memory_write with the same key when a decision changed rather than ended.",
			InputSchema: schema(`{"type":"object","properties":{"key":{"type":"string"},"reason":{"type":"string"}},"required":["key","reason"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var o memory.Op
				if err := json.Unmarshal(raw, &o); err != nil {
					return "", err
				}
				o.Op = "drop"
				return writeRecord(p, o)
			},
		},
		{
			Name:        "memory_history",
			Description: "Every change to one record entry: what it said before, what replaced it, and why.",
			InputSchema: schema(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Key string `json:"key"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				rec, err := p.LoadRecord()
				if err != nil {
					return "", err
				}
				var b strings.Builder
				if e, ok := rec.Entries[a.Key]; ok {
					fmt.Fprintf(&b, "Current: %s\n", memory.RenderEntry(a.Key, e))
				} else {
					fmt.Fprintf(&b, "No current entry %q.\n", a.Key)
				}
				for _, c := range rec.History(a.Key) {
					fmt.Fprintf(&b, "- %s %s %s (%s): was %q", c.At.Format("2006-01-02"), c.Op, c.Key, c.Source, c.Old)
					if c.New != "" {
						fmt.Fprintf(&b, " → %q", c.New)
					}
					if c.Reason != "" {
						fmt.Fprintf(&b, ". Why: %s", c.Reason)
					}
					b.WriteString("\n")
				}
				return b.String(), nil
			},
		},
		{
			Name: "transcript_search",
			Description: "Search the project's full session transcripts (both sides, tool calls shortened). Use it for exact wording or " +
				"detail the record and Now summary leave out. Returns refs for transcript_read.",
			InputSchema: schema(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				results, err := wake.Search(wake.Options{CorpusRoot: corpusRoot, Scope: p.Root, Query: a.Query, Limit: a.Limit})
				if err != nil {
					return "", err
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%d match(es)\n", len(results))
				for _, r := range results {
					fmt.Fprintf(&b, "\n%s  %s\n%s\n", r.Ref, r.Title, r.Excerpt)
				}
				return b.String(), nil
			},
		},
		{
			Name:        "transcript_read",
			Description: "Read a transcript chunk (transcript:<source>/<session>/<chunk>) or one cited item (codex:<8 hex>#L<line>, claude:<8 hex>#L<line>).",
			InputSchema: schema(`{"type":"object","properties":{"ref":{"type":"string"}},"required":["ref"]}`),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Ref string `json:"ref"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				return wake.Read(wake.Options{CorpusRoot: corpusRoot}, a.Ref)
			},
		},
	}
}

func writeRecord(p memory.Project, o memory.Op) (string, error) {
	if err := p.Init(); err != nil {
		return "", err
	}
	unlock, err := p.Lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	rec, err := p.LoadRecord()
	if err != nil {
		return "", err
	}
	res := rec.Apply([]memory.Op{o}, time.Now(), nil, "tool")
	if res.Skipped > 0 {
		if o.Op == "drop" {
			return "", fmt.Errorf("no entry %q", o.Key)
		}
		return "", fmt.Errorf("not written: kind must be decision, tried or preference, and text must be set")
	}
	if err := p.SaveRecord(rec); err != nil {
		return "", err
	}
	switch {
	case res.Dropped > 0:
		return "Dropped " + o.Key + ".", nil
	case res.Replaced > 0:
		return "Replaced " + o.Key + ": " + memory.RenderEntry(o.Key, rec.Entries[o.Key]), nil
	}
	return "Added " + o.Key + ": " + memory.RenderEntry(o.Key, rec.Entries[o.Key]), nil
}

func schema(s string) json.RawMessage {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("bad tool schema: " + err.Error())
	}
	b, _ := json.Marshal(v)
	return b
}

func asOf(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
