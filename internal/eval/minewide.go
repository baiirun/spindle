package eval

import (
	"sort"
	"strings"
	"time"

	"spindle/internal/corpus"
)

// The cue regex alone found only 92 of ~9,000 user messages: most real pickups
// don't say "continue". Two structural signals catch the rest: the first
// message of a session that follows recent work in the same directory, and
// the first message after a long pause inside one session.
const (
	wideRecentWork = 7 * 24 * time.Hour // a new session this soon after one in the same cwd may continue it
	wideSessionGap = 3 * time.Hour      // a pause this long inside a session may need the earlier context back
	wideMinChars   = 15                 // "ok", "yes" and "go" never carry a pickup on their own
)

const ResumeClassifyWideSystem = `You classify messages a user sent to an AI coding/design agent.
Keep a message only if continuing correctly DEPENDS on context from earlier work that isn't in the message
itself: an earlier session in the same project, or much earlier in a long session. Each message says why it
was flagged:
- cue: it uses resumption language ("continue", "where were we", "take over the other session").
- new_session: it opens a new session in a directory with recent earlier sessions. Keep it if it carries on
  that effort (implement the plan we made, next ticket, fix what we found, "ok let's do X" where X was set up
  earlier). Drop it if it starts something self-contained.
- after_gap: it's the first message after a long pause in the same session. Keep it if it picks the thread
  back up rather than starting something new.
Drop: ordinary follow-ups within the current exchange, "do it again", "try again", new self-contained tasks,
and questions about external things.`

// MineWideCandidates flags user messages that may pick up earlier work by cue,
// by opening a session soon after earlier work in the same directory, or by
// resuming a session after a long pause.
func MineWideCandidates(corpusRoot string) ([]ResumeCandidate, error) {
	paths, err := corpus.ChunkPaths(corpusRoot)
	if err != nil {
		return nil, err
	}
	type session struct {
		source, id, cwd string
		start, end      time.Time
		items           []corpus.Item // user items, in order
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
			s = &session{source: c.Source, id: c.Session, cwd: c.Cwd, start: c.Start, end: c.End}
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
				s.items = append(s.items, it)
			}
		}
	}
	byCwd := map[string][]*session{}
	for _, s := range sessions {
		sort.Slice(s.items, func(i, j int) bool { return s.items[i].Line < s.items[j].Line })
		byCwd[s.cwd] = append(byCwd[s.cwd], s)
	}

	var out []ResumeCandidate
	add := func(s *session, i int, why string) {
		it := s.items[i]
		out = append(out, ResumeCandidate{ItemID: it.ID, Source: s.source, Session: s.id, Cwd: s.cwd, AskedAt: it.Time, Text: it.Text, First: i == 0, Why: why})
	}
	for _, s := range sessions {
		recentBefore := false
		for _, o := range byCwd[s.cwd] {
			if o != s && o.end.Before(s.start) && s.start.Sub(o.end) <= wideRecentWork {
				recentBefore = true
				break
			}
		}
		for i, it := range s.items {
			text := strings.TrimSpace(it.Text)
			if len(text) > 2000 {
				continue
			}
			switch {
			case resumeCue.MatchString(text):
				add(s, i, "cue")
			case len(text) < wideMinChars:
			case i == 0 && recentBefore:
				add(s, i, "new_session")
			case i > 0 && it.Time.Sub(s.items[i-1].Time) >= wideSessionGap:
				add(s, i, "after_gap")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AskedAt.Before(out[j].AskedAt) })
	return out, nil
}
