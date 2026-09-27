package dream

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Format v3 has the model emit edit operations that code applies to a typed
// state. v2 rewrote the whole memory every step: ~140 s per step, and the
// model ignored the size cap (22k vs 12k). With operations, output is small,
// code enforces the caps, and a changed rule is always logged: code records
// the old text before replacing it, so silent rewrites can't happen.

const systemV3 = `You maintain the memory of one long-running project by folding new episodes into it. An episode
is a cited summary of a stretch of a session. You receive the current memory and the next episodes in time
order. Return the edit operations that bring the memory up to date, as JSON. Code applies them.

Operations (fill unused fields with "" or []):
- set_rule: add or replace a current rule or decision. key: short stable topic name (reuse the existing key
  to update it). text: the exact rule, value, or choice, stated fully enough to apply it (keep numbers, dice,
  table entries, wording). who: "user" only when the evidence shows the user stated or agreed to it,
  otherwise "agent proposed, not agreed". date: YYYY-MM-DD. why: one clause. cites: 1-2 item IDs.
- remove_rule: a rule no longer holds. key, reason, cites.
- upsert_thread: add or update an active line of work. key: thread name. status: active | parked.
  now, tried (what was tried or rejected, and why), open (unanswered question), next (concrete next step),
  cites.
- drop_thread: a thread is done or dormant. key, reason.

Rules:
- Only emit operations for things the new episodes change. No operation for unchanged items.
- Moving on is not agreeing: never mark an agent proposal as the user's.
- Cite item IDs exactly as they appear in the episodes (codex:<8 hex>#L<line>). Never invent one.
- Use only the current memory and the new episodes. Do not invent, and do not add advice.`

const opsSchema = `{"type":"object","properties":{"ops":{"type":"array","items":{"type":"object","properties":{"op":{"type":"string","enum":["set_rule","remove_rule","upsert_thread","drop_thread"]},"key":{"type":"string"},"text":{"type":"string"},"who":{"type":"string"},"date":{"type":"string"},"why":{"type":"string"},"reason":{"type":"string"},"status":{"type":"string"},"now":{"type":"string"},"tried":{"type":"string"},"open":{"type":"string"},"next":{"type":"string"},"cites":{"type":"array","items":{"type":"string"}}},"required":["op","key","text","who","date","why","reason","status","now","tried","open","next","cites"]}}},"required":["ops"]}`

// Caps enforced by code, not asked of the model.
const (
	maxThreads      = 6
	maxShownChanges = 10
	maxFieldChars   = 400
)

// Op is one edit operation from the model.
type Op struct {
	Op     string   `json:"op"`
	Key    string   `json:"key"`
	Text   string   `json:"text"`
	Who    string   `json:"who"`
	Date   string   `json:"date"`
	Why    string   `json:"why"`
	Reason string   `json:"reason"`
	Status string   `json:"status"`
	Now    string   `json:"now"`
	Tried  string   `json:"tried"`
	Open   string   `json:"open"`
	Next   string   `json:"next"`
	Cites  []string `json:"cites"`
}

// State is the typed project memory that operations edit.
type State struct {
	Rules   map[string]Rule   `json:"rules"`
	Threads map[string]Thread `json:"threads"`
	Changes []Change          `json:"changes"` // full log; rendering shows the latest few
	AsOf    time.Time         `json:"as_of"`
	Through string            `json:"through"`
}

type Rule struct {
	Text, Who, Date, Why string
	Cites                []string
	Step                 int
}

type Thread struct {
	Status, Now, Tried, Open, Next string
	Cites                          []string
	Step                           int
}

type Change struct {
	Date, Key, Old, New, Reason string
	Cites                       []string
}

func newState() *State {
	return &State{Rules: map[string]Rule{}, Threads: map[string]Thread{}}
}

// Apply edits the state. Replacing or removing a rule always logs the old
// text, so a changed belief stays visible. It returns how many ops it skipped.
func (s *State) Apply(ops []Op, step int, date string) (skipped int) {
	for _, o := range ops {
		key := strings.TrimSpace(o.Key)
		if key == "" {
			skipped++
			continue
		}
		cites := o.Cites
		if len(cites) > 2 {
			cites = cites[:2]
		}
		switch o.Op {
		case "set_rule":
			text := clip(o.Text)
			if text == "" {
				skipped++
				continue
			}
			if old, ok := s.Rules[key]; ok && old.Text != text {
				s.Changes = append(s.Changes, Change{Date: orDate(o.Date, date), Key: key, Old: old.Text, New: text, Reason: o.Why, Cites: cites})
			}
			s.Rules[key] = Rule{Text: text, Who: o.Who, Date: o.Date, Why: clip(o.Why), Cites: cites, Step: step}
		case "remove_rule":
			old, ok := s.Rules[key]
			if !ok {
				skipped++
				continue
			}
			s.Changes = append(s.Changes, Change{Date: orDate(o.Date, date), Key: key, Old: old.Text, New: "(removed)", Reason: o.Reason, Cites: cites})
			delete(s.Rules, key)
		case "upsert_thread":
			t := s.Threads[key]
			t.Status = o.Status
			set := func(dst *string, v string) {
				if v = strings.TrimSpace(v); v != "" {
					*dst = clip(v)
				}
			}
			set(&t.Now, o.Now)
			set(&t.Tried, o.Tried)
			set(&t.Open, o.Open)
			set(&t.Next, o.Next)
			if len(cites) > 0 {
				t.Cites = cites
			}
			t.Step = step
			s.Threads[key] = t
		case "drop_thread":
			if _, ok := s.Threads[key]; !ok {
				skipped++
				continue
			}
			delete(s.Threads, key)
		default:
			skipped++
		}
	}
	// Keep the most recently touched threads, active before parked.
	if len(s.Threads) > maxThreads {
		names := make([]string, 0, len(s.Threads))
		for n := range s.Threads {
			names = append(names, n)
		}
		sort.Slice(names, func(i, j int) bool {
			a, b := s.Threads[names[i]], s.Threads[names[j]]
			if (a.Status == "parked") != (b.Status == "parked") {
				return b.Status == "parked"
			}
			return a.Step > b.Step
		})
		for _, n := range names[maxThreads:] {
			delete(s.Threads, n)
		}
	}
	return skipped
}

// Render writes the state in the v2 Markdown shape.
func (s *State) Render(project string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — project memory\nas of: %s · folded through: %s\n\n## Current rules and decisions\n",
		project, s.AsOf.UTC().Format("2006-01-02 15:04"), s.Through)
	keys := make([]string, 0, len(s.Rules))
	for k := range s.Rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := s.Rules[k]
		fmt.Fprintf(&b, "- %s: %s — %s, %s; why: %s %s\n", k, r.Text, orNA(r.Who), orNA(r.Date), orNA(r.Why), cite(r.Cites))
	}
	b.WriteString("\n## Active threads\n")
	names := make([]string, 0, len(s.Threads))
	for n := range s.Threads {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return s.Threads[names[i]].Step > s.Threads[names[j]].Step })
	for _, n := range names {
		t := s.Threads[n]
		fmt.Fprintf(&b, "### %s · %s\n", n, orNA(t.Status))
		for _, f := range [][2]string{{"Now", t.Now}, {"Tried / rejected", t.Tried}, {"Open", t.Open}, {"Next", t.Next}} {
			if f[1] != "" {
				fmt.Fprintf(&b, "- %s: %s\n", f[0], f[1])
			}
		}
		if len(t.Cites) > 0 {
			fmt.Fprintf(&b, "- Evidence: %s\n", cite(t.Cites))
		}
	}
	b.WriteString("\n## Recently changed\n")
	for i := len(s.Changes) - 1; i >= 0 && i >= len(s.Changes)-maxShownChanges; i-- {
		c := s.Changes[i]
		fmt.Fprintf(&b, "- %s · %s: %q → %q, because %s %s\n", c.Date, c.Key, shorten(c.Old, 160), shorten(c.New, 160), orNA(c.Reason), cite(c.Cites))
	}
	return b.String()
}

func clip(s string) string { return shorten(strings.TrimSpace(s), maxFieldChars) }

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func cite(c []string) string {
	if len(c) == 0 {
		return ""
	}
	return "[" + strings.Join(c, ", ") + "]"
}

func orNA(s string) string {
	if strings.TrimSpace(s) == "" {
		return "n/a"
	}
	return s
}

func orDate(d, fallback string) string {
	if strings.TrimSpace(d) == "" {
		return fallback
	}
	return d
}
