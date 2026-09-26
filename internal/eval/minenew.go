package eval

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"spindle/internal/corpus"
)

// Spindle is for the moment a brand-new session starts on an existing effort:
// the context lives in earlier sessions and the harness doesn't know which.
// Pickups inside one session are better served by resuming that thread, so
// this miner only flags the opening request of a session that follows earlier
// sessions in the same directory, from either tool.
const (
	newSessionWindow   = 14 * 24 * time.Hour // earlier sessions this recent may hold the context
	newSessionMaxOpens = 3                   // the opening request is among the first few user messages
)

const NewSessionClassifySystem = `You classify the opening request of a new AI coding/design agent session.
Each request comes with the earlier sessions in the same project directory (their first and last user
messages). Keep a request only if doing it correctly DEPENDS on context from one of those earlier sessions
that the request itself doesn't carry: a plan, decision, investigation, ticket or half-finished change from
before ("implement the plan", "continue the migration", "next ticket", "fix what we found", "take over the
other session"). Drop it if it's self-contained, unrelated to the earlier sessions, or a general question.`

// MineNewSessionCandidates flags the opening request of each session that
// follows another session in the same directory within newSessionWindow.
func MineNewSessionCandidates(corpusRoot string) ([]ResumeCandidate, error) {
	paths, err := corpus.ChunkPaths(corpusRoot)
	if err != nil {
		return nil, err
	}
	type session struct {
		handle, source, id, cwd string
		start, end              time.Time
		users                   []corpus.Item
	}
	sessions := map[string]*session{}
	for _, p := range paths {
		c, err := corpus.ReadChunk(p)
		if err != nil {
			return nil, err
		}
		key := c.Source + ":" + c.Session
		s := sessions[key]
		if s == nil {
			s = &session{handle: key, source: c.Source, id: c.Session, cwd: c.Cwd, start: c.Start, end: c.End}
			sessions[key] = s
		}
		if c.Start.Before(s.start) {
			s.start = c.Start
		}
		if c.End.After(s.end) {
			s.end = c.End
		}
		for _, it := range c.Items {
			if it.Role == corpus.RoleUser {
				s.users = append(s.users, it)
			}
		}
	}
	byCwd := map[string][]*session{}
	for _, s := range sessions {
		sort.Slice(s.users, func(i, j int) bool { return s.users[i].Line < s.users[j].Line })
		byCwd[s.cwd] = append(byCwd[s.cwd], s)
	}
	var out []ResumeCandidate
	for _, s := range sessions {
		// The opening request: the first substantive user message.
		open := -1
		for i := 0; i < len(s.users) && i < newSessionMaxOpens; i++ {
			if t := strings.TrimSpace(s.users[i].Text); len(t) >= wideMinChars && len(t) <= 2000 {
				open = i
				break
			}
		}
		if open < 0 {
			continue
		}
		at := s.users[open].Time
		var earlier []*session
		for _, o := range byCwd[s.cwd] {
			if o != s && o.start.Before(at) && at.Sub(o.end) <= newSessionWindow && len(o.users) > 0 {
				earlier = append(earlier, o)
			}
		}
		if len(earlier) == 0 {
			continue
		}
		sort.Slice(earlier, func(i, j int) bool { return earlier[i].end.After(earlier[j].end) })
		if len(earlier) > 4 {
			earlier = earlier[:4]
		}
		var ctx strings.Builder
		for _, o := range earlier {
			last := o.users[0]
			for _, u := range o.users {
				if u.Time.Before(at) {
					last = u
				}
			}
			fmt.Fprintf(&ctx, "- %s (%s → %s): first %q; last before this: %q\n", o.handle,
				o.start.Format("2006-01-02"), o.end.Format("2006-01-02"), clip(oneLine(o.users[0].Text), 160), clip(oneLine(last.Text), 160))
		}
		it := s.users[open]
		out = append(out, ResumeCandidate{ItemID: it.ID, Source: s.source, Session: s.id, Cwd: s.cwd, AskedAt: it.Time,
			Text: it.Text, First: open == 0, Why: "new_session", Context: ctx.String()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AskedAt.Before(out[j].AskedAt) })
	return out, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
