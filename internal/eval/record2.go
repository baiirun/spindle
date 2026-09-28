package eval

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"spindle/internal/corpus"
	"spindle/internal/llm"
)

// Decision record v2 ("record2"). The first record's audit found the model adds
// entries and almost never revises old ones (7 replaces, 0 drops in 123
// compactions), leaving outdated rules next to their replacements, and that it
// filed open questions and unapproved proposals as decisions. Two fixes:
//   - Approval gate: a decision or preference is kept only with the user's own
//     words quoted, and code checks the quote is in a user message.
//   - Supersession check: after each step, every new or changed entry is shown
//     the existing entries on the same topic (found with the record's own
//     search) and the model says which ones it replaces or narrows. Code
//     applies those, and the new entry records what it replaced.
// Still no size cap and no eviction by code.

func IsRecord(version string) bool { return version == "record" || version == "record2" }

const recordSystemV2 = `You keep the decision record of one long-running conversation, as the assistant in it. The record is a
long, detailed log that a future assistant searches when it needs an earlier decision, its reason, or whether
something was already tried; it is not shown in the prompt, so it has no size limit. Another summary covers the
current state, the next step and open questions. You receive the current record and the conversation since the last
compaction. Return the edit operations that bring the record up to date, as JSON. Code applies them.

Entry kinds:
- decision: a decision or rule currently in force that the USER made. text: what was decided, stated fully enough
  to apply it without the conversation (keep exact values, names, numbers, wording, and the scope it applies to).
  why: the reason, in the user's reasoning where possible. who: "user" (the user stated it) or "user approved" (the
  assistant proposed it and the user clearly agreed to that specific proposal). user_words: the user's own words
  that state or approve it, copied verbatim from a user message (a short exact quote; use "..." between pieces).
- tried: something that was actually tried and failed, or an option that was explicitly turned down, with what
  happened and why. who: who rejected it. Something requested, pending or not yet answered is not "tried".
- preference: a standing instruction or correction from the user about how the assistant should work (process,
  tone, what to check, what not to do), meant to apply from now on. user_words: the user's words, verbatim. A
  preference is never a question, a design idea, a product decision or a one-off request.

Every kind: an open question, something the user asked, wondered about or wants researched, and anything still
undecided never goes in the record under any kind. Quoting the user asking a question is not approval.

Approval gate (code checks it: a decision or preference whose user_words are not found verbatim in a user message
is thrown away):
- Record a decision only when the user stated it or clearly approved it. "yes", "do it", "go with B", "sounds good"
  directly answering that proposal count as approval. The user moving on, asking a follow-up, saying "interesting",
  "maybe", "let me think", or approving something else does not.
- The assistant's own proposals, recommendations, drafts, options and plans the user has not approved are not
  decisions, however detailed. Open questions ("should X be Y?"), ideas under discussion and things the user is
  still weighing are not decisions. Leave all of these out; the other summary carries them.
- An implementation detail the assistant chose on its own is not a decision unless the user approved it.

Operations (fill unused fields with "" or []):
- add: a new entry. key: a short, specific, searchable topic name (the words someone would search for).
- replace: an existing entry changed. Reuse its exact key. Code keeps the old text as "replaced", so state the new
  version fully and say in why what changed and why.
- drop: an entry no longer holds. key, reason.

Rules:
- Be detailed: one entry per distinct decision, not merged summaries. Old decisions still in force stay, however old.
- When the conversation changes, reverses, narrows or abandons an existing entry, replace or drop it. A separate
  check will also show each new entry the existing ones on the same topic, but catch what you can here.
- Point to files instead of copying their content: name the file or artifact, don't paste it.
- Cite 1-2 item IDs exactly as they appear in the conversation (codex:<8 hex>#L<line>) for every add and replace,
  including the user message the quote comes from. Never invent one.

What never goes in the record:
- progress, status, milestones, completed or in-flight work, commits, test results;
- open questions, proposals awaiting an answer, pending approvals, and next steps;
- one-off instructions for a single task ("commit this", "fix 5-8").
A decision is a choice that constrains future work and would still matter weeks later.`

// gate drops decisions and preferences the user did not state or approve in
// their own words, and logs each one it drops.
func (l *Ledger) gate(ops []ledgerOp, step int, said string) []ledgerOp {
	var kept []ledgerOp
	for _, o := range ops {
		if o.Op == "drop" || o.Kind == "tried" {
			kept = append(kept, o)
			continue
		}
		reason := ""
		who := strings.ToLower(strings.TrimSpace(o.Who))
		switch {
		case o.Kind == "decision" && who != "user" && who != "user approved":
			reason = "not made by the user: " + o.Who
		case strings.TrimSpace(o.UserWords) == "":
			reason = "no user words quoted"
		case !quoteFound(said, o.UserWords):
			reason = "quote not in any user message: " + clipTo(o.UserWords, 120)
		}
		if reason != "" {
			l.Log = append(l.Log, LedgerChange{Step: step, Op: "gated", Key: o.Key, New: o.Text, Reason: reason})
			continue
		}
		kept = append(kept, o)
	}
	return kept
}

// userSaid is every user message before cut, normalized for quote matching.
func userSaid(s corpus.Session, cut time.Time) string {
	return userSaidNorm(s, time.Time{}, cut)
}

func userSaidNorm(s corpus.Session, from, to time.Time) string {
	var b strings.Builder
	for _, it := range s.Items {
		if it.Role == corpus.RoleUser && !it.Time.Before(from) && it.Time.Before(to) {
			b.WriteString(" " + normQuote(it.Text) + " |")
		}
	}
	return b.String()
}

// userSaidBetween is the user's messages in [from, to), verbatim with IDs, for the supersession check.
func userSaidBetween(s corpus.Session, from, to time.Time) string {
	return renderItems(s, from, to, corpus.RoleUser)
}

// normQuote lowercases and keeps only letters and digits, single-spaced, so a
// quote matches despite punctuation, quotes or line breaks.
func normQuote(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

// quoteFound reports whether every piece of the quote (split at ellipses) is in said.
func quoteFound(said, quote string) bool {
	found := 0
	for _, piece := range strings.FieldsFunc(strings.ReplaceAll(quote, "...", "…"), func(r rune) bool { return r == '…' }) {
		p := normQuote(piece)
		if p == "" {
			continue
		}
		if !strings.Contains(said, " "+p) {
			return false
		}
		found++
	}
	return found > 0
}

const supersedeSystem = `You check a decision record for entries that newer entries have overtaken. You get entries that were just
added or changed, each with the existing entries on the same topic that a search found, and the user's messages
from the stretch of conversation the new entries came from. For each new entry, say which of its candidates it
replaces or narrows:
- replaces: the candidate no longer holds at all; the new entry supersedes it (a changed value, a reversed rule, a
  mechanic swapped for another, a duplicate stating the same decision).
- narrows: the candidate still partly holds, but the new entry changes part of it or limits its scope. Give the
  candidate's corrected full text in narrowed_text, keeping everything that still holds.
Leave a candidate alone when it is compatible with the new entry, is about something else, or you can't tell. Only
act on what the entries and the user's messages show. Return JSON; list only candidates you act on.`

const supersedeSchema = `{"type":"object","properties":{"checks":{"type":"array","items":{"type":"object","properties":{"key":{"type":"string"},"overtakes":{"type":"array","items":{"type":"object","properties":{"key":{"type":"string"},"how":{"type":"string","enum":["replaces","narrows"]},"narrowed_text":{"type":"string"},"reason":{"type":"string"}},"required":["key","how","narrowed_text","reason"]}}},"required":["key","overtakes"]}}},"required":["checks"]}`

// supersedeCandidates is how many same-topic entries each new entry is checked against.
const supersedeCandidates = 6

// supersede runs the supersession check for the entries ops added or changed.
func (l *Ledger) supersede(ctx context.Context, ops []ledgerOp, step int, said string) error {
	touched := map[string]bool{}
	for _, o := range ops {
		k := strings.TrimSpace(o.Key)
		if e, ok := l.Entries[k]; ok && o.Op != "drop" && e.Step == step && e.Kind != "tried" {
			touched[k] = true
		}
	}
	keys := make([]string, 0, len(touched))
	for k := range touched {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cands := map[string][]string{}
	var b strings.Builder
	for _, k := range keys {
		e := l.Entries[k]
		var cs []string
		for _, h := range SearchRecord(*l, k+" "+e.Text, supersedeCandidates+len(touched)) {
			if h.Key == k || touched[h.Key] || h.Entry.Kind == "tried" || len(cs) >= supersedeCandidates {
				continue
			}
			cs = append(cs, h.Key)
		}
		if len(cs) == 0 {
			continue
		}
		cands[k] = cs
		fmt.Fprintf(&b, "<new_entry>\n%s\n<candidates>\n%s\n</candidates>\n</new_entry>\n\n", l.renderKeys([]string{k}), l.renderKeys(cs))
	}
	if len(cands) == 0 {
		return nil
	}
	prompt := b.String() + "<user_messages>\n" + said + "</user_messages>"
	var out struct {
		Checks []struct {
			Key       string `json:"key"`
			Overtakes []struct {
				Key          string `json:"key"`
				How          string `json:"how"`
				NarrowedText string `json:"narrowed_text"`
				Reason       string `json:"reason"`
			} `json:"overtakes"`
		} `json:"checks"`
	}
	req := llm.Request{Model: llm.Reader, System: supersedeSystem, Prompt: prompt, Schema: supersedeSchema, Timeout: 20 * time.Minute}
	if _, err := llm.JSONRequest(ctx, req, &out); err != nil {
		return fmt.Errorf("supersede: %w", err)
	}
	for _, c := range out.Checks {
		newKey := strings.TrimSpace(c.Key)
		for _, o := range c.Overtakes {
			oldKey := strings.TrimSpace(o.Key)
			old, ok := l.Entries[oldKey]
			if !ok || !contains(cands[newKey], oldKey) {
				l.Skipped++
				continue
			}
			ne := l.Entries[newKey]
			switch o.How {
			case "replaces":
				ne.Replaced = joinNonEmpty(ne.Replaced, fmt.Sprintf("%s: %q (%s)", oldKey, old.Text, old.Date))
				l.Entries[newKey] = ne
				delete(l.Entries, oldKey)
				l.Log = append(l.Log, LedgerChange{Step: step, Op: "superseded", Key: oldKey, Old: old.Text, New: newKey, Reason: o.Reason})
			case "narrows":
				text := strings.TrimSpace(o.NarrowedText)
				if text == "" || text == old.Text {
					l.Skipped++
					continue
				}
				old.Replaced = joinNonEmpty(fmt.Sprintf("%q (%s), narrowed by %s", old.Text, old.Date, newKey), old.Replaced)
				old.Text, old.Step = text, step
				l.Entries[oldKey] = old
				l.Log = append(l.Log, LedgerChange{Step: step, Op: "narrowed", Key: oldKey, Old: old.Replaced, New: text, Reason: o.Reason})
			default:
				l.Skipped++
			}
		}
	}
	return nil
}

func (l Ledger) renderKeys(keys []string) string {
	sub := Ledger{Entries: map[string]LedgerEntry{}}
	for _, k := range keys {
		sub.Entries[k] = l.Entries[k]
	}
	var lines []string
	for _, line := range strings.Split(sub.Render(), "\n") {
		if strings.HasPrefix(line, "- ") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
