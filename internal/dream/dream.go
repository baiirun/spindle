// Package dream folds a project's episodes, in event order, into project
// memory: the current working state, a history index with pointers, and a log
// of superseded beliefs. Each step reads only the previous memory and the new
// episodes, so the memory as of any time T is the fold of episodes before T.
package dream

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"spindle/internal/episode"
	"spindle/internal/llm"
)

// Formats are the memory contracts under test. Each names its own output
// directory, so snapshots from different formats never mix.
var Formats = map[string]Format{
	"v1": {Version: "project-memory-v1", System: systemV1},
	// v2: v1 held few specifics at 45k characters; most space went to ~500
	// citations, 10-18 threads and an ever-growing History. v2 keeps exact
	// current rules, caps threads and size (enforced by code), and leaves
	// history to an index code builds from episode titles.
	"v2": {Version: "project-memory-v2", System: systemV2, MaxChars: 12000, CodeHistory: true},
}

// Format is one memory contract.
type Format struct {
	Version     string
	System      string
	MaxChars    int  // 0: no enforced cap
	CodeHistory bool // write a per-step history index built from episode titles
}

const systemV1 = `You maintain the memory of one long-running project by folding new episodes into it. An episode
is a cited summary of a stretch of a session. You receive the previous memory and the next episodes in time
order. Return the complete updated memory and nothing else, in exactly this Markdown format:

# <project> — project memory
as of: <time of the last folded episode> · folded through: episode <NNNN>

## Working state
### <thread name> · status: active | parked | done
- Now: where it stands, 1–3 lines [cites]
- Decided: <decision> (<who>, <date>) [cites]
- Tried / rejected: <what, why> [cites]
- Open: <unanswered question> [cites]
- Next: <concrete next step> [cites]

## History
- <date or date range> <one line: what that stretch of work was and how it ended> → ep <NNNN>[–<NNNN>]

## Superseded
- <date>: "<old belief>" → <new belief>, because <reason> [cites]

Rules:
- Cite with the transcript item IDs exactly as they appear in the episodes
  (they look like codex:<8 hex>#L<line>; copy them, never invent one or reuse an ID from elsewhere).
  Every Now/Decided/Tried/Open/Next bullet and every Superseded entry needs at least one citation.
- Who decided: write "user" only when the evidence shows the user stated or agreed to it. Otherwise write
  "agent proposed, not agreed". Moving on is not agreeing.
- Keep 3–8 threads. A thread that is done or parked for good moves to History as one line; never delete
  History lines. Merge adjacent History lines only to stay under the size limit.
- When new evidence changes an earlier belief or status, add a Superseded entry. Never silently overwrite.
- Keep exact numbers, names, rules and identifiers from the episodes when they matter for continuing.
- Use only the previous memory and the new episodes. Do not invent, and do not add advice.
- Stay under ` + "10000" + ` characters.`

const systemV2 = `You maintain the memory of one long-running project by folding new episodes into it. An episode
is a cited summary of a stretch of a session. You receive the previous memory and the next episodes in time
order. A separate history index already lists every episode by date and title, so this memory holds only
the CURRENT state. Return the complete updated memory and nothing else, in exactly this Markdown format:

# <project> — project memory
as of: <time of the last folded episode> · folded through: episode <NNNN>

## Current rules and decisions
- <topic>: <the exact current rule, value, or choice, stated fully enough to apply it> — <who>, <date>; why: <one clause> [cite]

## Active threads
### <thread name> · active | parked
- Now: <where it stands> [cite]
- Tried / rejected: <what, and why> [cite]
- Open: <unanswered question>
- Next: <concrete next step>

## Recently changed
- <date>: "<old>" → "<new>", because <reason> [cite]

Rules:
- Current rules and decisions is the core. Keep exact numbers, dice, table entries, names and wording that a
  person would need to apply the rule, not a description of it. Update an entry in place when it changes,
  and add the change to Recently changed.
- Who: "user" only when the evidence shows the user stated or agreed to it; otherwise "agent proposed, not
  agreed". Moving on is not agreeing.
- At most 6 active threads. Drop threads that are done or dormant; the history index keeps them findable.
- Recently changed keeps the latest 10 changes; older ones may be dropped.
- One citation per bullet, two at most, copied exactly from the episodes (codex:<8 hex>#L<line>). Never
  invent or reuse an ID from elsewhere.
- Use only the previous memory and the new episodes. Do not invent, and do not add advice.
- Stay under 12000 characters.`

// Options configures one fold.
type Options struct {
	Project     string
	EpisodeRoot string
	Sessions    []string    // "source:session" handles whose episodes belong to the project
	Batch       int         // episodes per fold step
	Cuts        []time.Time // snapshot boundaries: a step never spans one
	Until       time.Time   // stop before episodes ending at or after this time (zero: fold all)
	Out         string      // directory for step snapshots and memory.md
	Model       string
	Format      Format
}

// Step is one saved snapshot of the memory.
type Step struct {
	N        int       `json:"n"`
	Through  string    `json:"through"` // last folded episode ref
	AsOf     time.Time `json:"as_of"`   // end time of the last folded episode
	Path     string    `json:"path"`
	Chars    int       `json:"chars"`
	BadCites int       `json:"bad_cites"` // citations not found in any folded episode
}

// Fold runs the fold and writes one snapshot per step plus memory.md.
func Fold(ctx context.Context, o Options) ([]Step, error) {
	if o.Batch <= 0 {
		o.Batch = 8
	}
	var eps []episode.Episode
	for _, h := range o.Sessions {
		source, session, ok := strings.Cut(h, ":")
		if !ok {
			return nil, fmt.Errorf("dream: session %q is not source:session", h)
		}
		es, err := episode.ReadSession(o.EpisodeRoot, source, session)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if e.Status != episode.StatusSourceOnly && (o.Until.IsZero() || e.End.Before(o.Until)) {
				eps = append(eps, e)
			}
		}
	}
	if len(eps) == 0 {
		return nil, fmt.Errorf("dream: no episodes to fold")
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].End.Before(eps[j].End) })
	if err := os.MkdirAll(filepath.Join(o.Out, "steps"), 0o755); err != nil {
		return nil, err
	}

	var (
		steps  []Step
		memory string
		folded []episode.Episode
		known  = map[string]bool{}
	)
	for _, batch := range batches(eps, o.Batch, o.Cuts) {
		var b strings.Builder
		fmt.Fprintf(&b, "<project>%s</project>\n<previous_memory>\n%s\n</previous_memory>\n<new_episodes>\n", o.Project, orNone(memory))
		for _, e := range batch {
			text, err := compact(o.EpisodeRoot, e)
			if err != nil {
				return steps, err
			}
			b.WriteString(text)
			for _, id := range citeRe.FindAllString(text, -1) {
				known[id] = true
			}
		}
		b.WriteString("</new_episodes>")
		req := llm.Request{Model: o.Model, System: o.Format.System, Prompt: b.String(), Timeout: 15 * time.Minute}
		res, err := llm.Run(ctx, req)
		if err != nil {
			return steps, fmt.Errorf("dream step %d: %w", len(steps)+1, err)
		}
		memory = strings.TrimSpace(res.Text)
		if o.Format.MaxChars > 0 && len(memory) > o.Format.MaxChars {
			// The model ignores size limits as the project grows; ask once to compress.
			req.Prompt = fmt.Sprintf("<memory_over_limit chars=%d limit=%d>\n%s\n</memory_over_limit>\n"+
				"Rewrite this memory in the same format under %d characters: merge or drop dormant threads, trim "+
				"Recently changed, and shorten wording, but keep every current rule exact.", len(memory), o.Format.MaxChars, memory, o.Format.MaxChars)
			if res, err = llm.Run(ctx, req); err != nil {
				return steps, fmt.Errorf("dream step %d compress: %w", len(steps)+1, err)
			}
			memory = strings.TrimSpace(res.Text)
		}
		last := batch[len(batch)-1]
		s := Step{N: len(steps) + 1, Through: fmt.Sprintf("%s:%s/%04d", last.Source, last.Session, last.Chunk), AsOf: last.End,
			Chars: len(memory), BadCites: badCites(memory, known)}
		s.Path = filepath.Join(o.Out, "steps", fmt.Sprintf("%03d.md", s.N))
		header := fmt.Sprintf("---\nproject: %s\nprompt: %s\nstep: %d\nthrough: %s\nas_of: %s\nchars: %d\nbad_cites: %d\n---\n\n",
			o.Project, o.Format.Version, s.N, s.Through, s.AsOf.UTC().Format(time.RFC3339), s.Chars, s.BadCites)
		if err := os.WriteFile(s.Path, []byte(header+memory+"\n"), 0o644); err != nil {
			return steps, err
		}
		if o.Format.CodeHistory {
			folded = append(folded, batch...)
			if err := os.WriteFile(strings.TrimSuffix(s.Path, ".md")+".history.md", []byte(historyIndex(o.Project, folded)), 0o644); err != nil {
				return steps, err
			}
		}
		steps = append(steps, s)
	}
	return steps, os.WriteFile(filepath.Join(o.Out, "memory.md"), []byte(memory+"\n"), 0o644)
}

// historyIndex lists every folded episode, newest first, as a dated title and a
// pointer. Code builds it so history can never be dropped or reworded.
func historyIndex(project string, eps []episode.Episode) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — history index\n\n", project)
	for i := len(eps) - 1; i >= 0; i-- {
		e := eps[i]
		fmt.Fprintf(&b, "- %s · %s → episode:%s/%s/%04d\n", e.Start.UTC().Format("2006-01-02"), e.Title, e.Source, e.Session, e.Chunk)
	}
	return b.String()
}

// AsOf returns the latest snapshot folded entirely before t.
func AsOf(steps []Step, t time.Time) (Step, bool) {
	var best Step
	found := false
	for _, s := range steps {
		if s.AsOf.Before(t) && (!found || s.AsOf.After(best.AsOf)) {
			best, found = s, true
		}
	}
	return best, found
}

// batches groups episodes in order, closing a batch at the size limit or
// before an episode that ends after the next cut, so each cut has a snapshot
// containing exactly the episodes that ended before it.
func batches(eps []episode.Episode, size int, cuts []time.Time) [][]episode.Episode {
	cs := append([]time.Time(nil), cuts...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].Before(cs[j]) })
	var out [][]episode.Episode
	var cur []episode.Episode
	ci := 0
	for _, e := range eps {
		for ci < len(cs) && !e.End.Before(cs[ci]) {
			if len(cur) > 0 {
				out, cur = append(out, cur), nil
			}
			ci++
		}
		cur = append(cur, e)
		if len(cur) == size {
			out, cur = append(out, cur), nil
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// compact renders an episode for the fold: a one-line header, then the
// episode's own body (purpose, observations, outputs, open threads, links)
// without its storage frontmatter. episode.ReadPath keeps only metadata, so
// the body is read from the file.
func compact(root string, e episode.Episode) (string, error) {
	b, err := os.ReadFile(episode.Path(root, e.Source, e.Session, e.Chunk))
	if err != nil {
		return "", err
	}
	body := string(b)
	if parts := strings.SplitN(body, "---\n", 3); len(parts) == 3 && parts[0] == "" {
		body = parts[2]
	}
	if i := strings.Index(body, "\n## "); i >= 0 {
		body = body[i+1:] // drop the title line; the header below carries it
	}
	return fmt.Sprintf("\n## ep %04d · %s → %s · %s\n%s", e.Chunk, e.Start.UTC().Format("2006-01-02 15:04"),
		e.End.UTC().Format("2006-01-02 15:04"), e.Title, strings.ReplaceAll(strings.TrimSpace(body), "\n## ", "\n### ")), nil
}

var citeRe = regexp.MustCompile(`(codex|claude):[0-9a-f]{8}#L\d+`)

// badCites counts citations that don't match any item cited by a folded
// episode: the memory can't have legitimately learned them.
func badCites(memory string, known map[string]bool) int {
	n := 0
	for _, c := range citeRe.FindAllString(memory, -1) {
		if !known[c] {
			n++
		}
	}
	return n
}

func orNone(s string) string {
	if s == "" {
		return "(none yet: this is the first step)"
	}
	return s
}
