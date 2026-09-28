package eval

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"spindle/internal/corpus"
	"spindle/internal/llm"
)

// Decision record: the two-part ledger moved out of the prompt. The first two
// rounds showed a long, specific ledger carries recall but overflows any prompt
// budget, and eviction or merging then decides what it keeps. The record has no
// budget, no eviction and no clipping; the summary is the Now part alone, and
// the answering agent searches the record with `spin decisions`.

const recordSystem = `You keep the decision record of one long-running conversation, as the assistant in it. The record is a
long, detailed log that a future assistant searches when it needs an earlier decision, its reason, or whether
something was already tried; it is not shown in the prompt, so it has no size limit. Another summary covers the
current state and next step. You receive the current record and the conversation since the last compaction. Return
the edit operations that bring the record up to date, as JSON. Code applies them.

Entry kinds:
- decision: a decision or rule currently in force. text: what was decided, stated fully enough to apply it without
  the conversation (keep exact values, names, numbers, wording, and the scope it applies to). why: the reason, in
  the user's reasoning where possible. who: "user" (the user stated it), "user approved" (the assistant proposed it
  and the user agreed; put the user's approving words in user_words, verbatim), or "assistant" (an implementation
  choice the assistant made and the user did not object to).
- tried: something tried or rejected, what happened, and why it failed or was turned down. who: who rejected it.
- preference: a standing preference or correction from the user about how to work. Put the user's words in
  user_words.

Operations (fill unused fields with "" or []):
- add: a new entry. key: a short, specific, searchable topic name (the words someone would search for).
- replace: an existing entry changed. Reuse its exact key. Code keeps the old text as "replaced", so state the new
  version fully and say in why what changed and why.
- drop: an entry no longer holds. key, reason. Prefer replace when a decision was changed rather than abandoned, so
  the old version stays visible. A reversed decision can also become a "tried" entry.

Rules:
- Be detailed: one entry per distinct decision, not merged summaries. Old decisions still in force stay, however old.
- Only replace or drop an entry when the conversation actually changed, reversed or abandoned it. Check every
  existing entry against the new conversation; a stale entry is the worst failure. No operation for unchanged
  entries, and never drop an entry to save space.
- Distinguish user decisions from assistant proposals. Do not turn a proposal into a commitment: an assistant
  proposal the user has not accepted is not a decision. Moving on is not agreeing.
- Point to files instead of copying their content: name the file or artifact, don't paste it.
- Cite 1-2 item IDs exactly as they appear in the conversation (codex:<8 hex>#L<line>) for every add and replace.
  Never invent one.

What never goes in the record (the other summary rewrites it every compaction):
- progress, status, milestones, completed or in-flight work, commits, test results ("M4 is complete", "slices 1-3
  landed", "the sheet was rebuilt");
- open questions, pending approvals, and next steps;
- one-off instructions for a single task ("commit this", "fix 5-8").
A decision is a choice that constrains future work and would still matter weeks later.`

// RecordPath is the cached decision record as of the trial's last real compaction.
func RecordPath(t Trial) (string, error) {
	cuts, err := compactionsOf(NativePrior(t))
	if err != nil {
		return "", err
	}
	k := -1
	for i, c := range cuts {
		if c.Before(t.AskedAt) {
			k = i
		}
	}
	if k < 0 {
		return "", fmt.Errorf("record: no compaction before %s", t.AskedAt)
	}
	return ledgerPath(ledgerDir(filepath.Join(twoPartCache, NativePrior(t)), "record"), k+1), nil
}

// RecordHit is one search result.
type RecordHit struct {
	Key   string
	Entry LedgerEntry
	Score float64
}

// SearchRecord ranks record entries against the query by term overlap,
// weighted by rarity (idf), with the key counted twice. Words are lightly
// stemmed, and terms of four or more letters also match words they prefix.
func SearchRecord(l Ledger, query string, limit int) []RecordHit {
	q := searchTerms(query)
	if len(q) == 0 {
		return nil
	}
	docs := map[string][]string{}
	df := map[string]int{}
	for key, e := range l.Entries {
		words := searchTerms(strings.Join([]string{key, key, e.Text, e.Why, e.Who, e.UserWords, e.Replaced, e.Kind}, " "))
		docs[key] = words
		seen := map[string]bool{}
		for _, t := range q {
			if !seen[t] && matchesAny(t, words) {
				seen[t] = true
				df[t]++
			}
		}
	}
	n := float64(len(docs))
	var hits []RecordHit
	for key, words := range docs {
		score := 0.0
		for _, t := range q {
			tf := 0
			for _, w := range words {
				if termMatch(t, w) {
					tf++
				}
			}
			if tf > 0 {
				score += math.Log(1+n/float64(df[t])) * (1 + math.Log(float64(tf)))
			}
		}
		if score > 0 {
			hits = append(hits, RecordHit{Key: key, Entry: l.Entries[key], Score: score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Key < hits[j].Key
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// RenderHits writes search results in the ledger's line format, one entry per block.
func RenderHits(l Ledger, hits []RecordHit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Decision record as of %s (%d entries). %d match(es):\n", l.AsOf.UTC().Format("2006-01-02 15:04Z"), len(l.Entries), len(hits))
	titles := map[string]string{}
	for _, k := range ledgerKinds {
		titles[k.Kind] = k.Title
	}
	for _, h := range hits {
		one := Ledger{Entries: map[string]LedgerEntry{h.Key: h.Entry}}
		line := one.Render()
		for _, l := range strings.Split(line, "\n") {
			if strings.HasPrefix(l, "- ") {
				fmt.Fprintf(&b, "\n[%s] %s\n", titles[h.Entry.Kind], strings.TrimPrefix(l, "- "))
			}
		}
	}
	return b.String()
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields("the a an and or of to in on for with is are was were be been it its this that these those as at by from we you i our your did do does what which who why how when about any not no yes into than then there their they them he she his her has have had but if so can will would should could up out") {
		stopwords[w] = true
	}
}

func searchTerms(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) > 1 && !stopwords[w] {
			out = append(out, stem(w))
		}
	}
	return out
}

// stem strips a common English suffix so "stressed", "stresses" and "stress" meet.
func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 4 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

func termMatch(q, w string) bool {
	return w == q || (len(q) >= 4 && strings.HasPrefix(w, q))
}

func matchesAny(q string, words []string) bool {
	for _, w := range words {
		if termMatch(q, w) {
			return true
		}
	}
	return false
}

const recordAuditSystem = `You audit the decision record of one long-running conversation. You get the record (entries with a date and
citations), every user message in the conversation, and the latest summary of where the work stands. Flag entries
that are wrong as of the end of the conversation:
- stale: a later message or the latest summary changes, reverses, or abandons it, and the entry does not reflect that;
- not a decision: an open question, a proposal the user never accepted, progress or status, or a one-off instruction.
Only flag what the evidence shows; say which later item ID shows it. Do not flag entries that are merely incomplete.`

const recordAuditSchema = `{"type":"object","properties":{"flags":{"type":"array","items":{"type":"object","properties":{"key":{"type":"string"},"problem":{"type":"string","enum":["stale","not a decision"]},"evidence":{"type":"string"},"explanation":{"type":"string"}},"required":["key","problem","evidence","explanation"]}}},"required":["flags"]}`

// RecordFlag is one entry the audit found wrong.
type RecordFlag struct {
	Key         string `json:"key"`
	Problem     string `json:"problem"`
	Evidence    string `json:"evidence"`
	Explanation string `json:"explanation"`
}

// AuditRecord checks the session's last cached record against the user's
// messages and the last cached Now part. It is a spot check, not a full
// review: changes that only show in the assistant's turns can be missed.
func AuditRecord(ctx context.Context, session string) (Ledger, []RecordFlag, error) {
	dir := filepath.Join(twoPartCache, session)
	steps, _ := filepath.Glob(filepath.Join(ledgerDir(dir, "record"), "ledger-*.json"))
	nows, _ := filepath.Glob(filepath.Join(dir, "now-*.md"))
	if len(steps) == 0 || len(nows) == 0 {
		return Ledger{}, nil, fmt.Errorf("audit %s: record or Now part not built", session)
	}
	sort.Strings(steps)
	sort.Strings(nows)
	l, err := readLedger(steps[len(steps)-1])
	if err != nil {
		return l, nil, err
	}
	now, err := os.ReadFile(nows[len(nows)-1])
	if err != nil {
		return l, nil, err
	}
	path, err := findCodexRollout(session)
	if err != nil {
		return l, nil, err
	}
	s, _, err := corpus.ReadCodexSession(path)
	if err != nil {
		return l, nil, err
	}
	msgs := renderItems(s, time.Time{}, l.AsOf, corpus.RoleUser)
	prompt := "<record>\n" + l.Render() + "\n</record>\n\n<user_messages>\n" + msgs + "</user_messages>\n\n<latest_summary>\n" + string(now) + "\n</latest_summary>"
	var out struct {
		Flags []RecordFlag `json:"flags"`
	}
	_, err = llm.JSONRequest(ctx, llm.Request{Model: llm.Judge, Effort: "medium", System: recordAuditSystem, Prompt: prompt, Schema: recordAuditSchema, Timeout: 30 * time.Minute}, &out)
	return l, out.Flags, err
}
