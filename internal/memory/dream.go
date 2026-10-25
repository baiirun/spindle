package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Dreaming tidies the record across sessions. Extraction sees one stretch of
// conversation at a time, and the evals showed the model rarely revises old
// entries from there (7 replaces in 123 compactions), so outdated rules pile up
// beside their replacements. The dream pass reads the whole record and marks
// entries that newer ones overtook, as the supersession check in eval record2
// did (it worked: 15 replacements, 63 narrowings). It never drops an entry for
// age or size, which is what cost the consolidating ledger its recall.

// Dream gate, the same shape as Claude Code's auto_dream: enough time and
// enough sessions since the last pass.
const (
	DreamEvery    = 24 * time.Hour
	DreamSessions = 5
	dreamBatch    = 120_000 // record characters per model call
)

// DreamDue reports whether the gate is open.
func DreamDue(s State, now time.Time, every time.Duration, sessions int) bool {
	if len(s.SessionsSince) < sessions {
		return false
	}
	return s.LastDream.IsZero() || now.Sub(s.LastDream) >= every
}

const dreamSystem = `You tidy the decision record of one long-running project. Entries are decisions in force, things tried or
rejected, and the user's standing preferences, each with a date, who decided, and cites. They were written one
stretch of conversation at a time, so an older entry may have been overtaken by a newer one without being updated.
Return edit operations, as JSON, for entries that newer entries overtook. Code applies them.

- replace: an older entry still partly holds but a newer one changed part of it or limited its scope. Reuse the old
  key, state its corrected full text (keep everything that still holds and its exact values), keep its kind, who and
  cites, and say in why which newer entry changed it.
- drop: an older entry no longer holds at all because a newer entry superseded it (a changed value, a reversed rule,
  a mechanic swapped for another, or a duplicate of the same decision). Give the newer entry's key in reason.

Rules:
- Only act when the entries themselves show the change: a newer date and a conflicting or duplicate decision. When
  two entries are compatible, about different things, or you can't tell, leave both alone.
- Never drop an entry because it is old, long, or seems minor. Old decisions still in force are what the record is for.
- Never invent new decisions. Fill unused fields with "" or [].`

// Dream runs one pass over the project's record and resets the gate.
func Dream(ctx context.Context, p Project, m Model, now time.Time) (ApplyResult, error) {
	var total ApplyResult
	rec, err := p.LoadRecord()
	if err != nil {
		return total, err
	}
	var ops []Op
	for _, batch := range dreamBatches(rec, dreamBatch) {
		var out struct {
			Ops []Op `json:"ops"`
		}
		if err := m.JSON(ctx, dreamSystem, "<record>\n"+batch+"\n</record>", opsSchema, &out); err != nil {
			return total, fmt.Errorf("dream: %w", err)
		}
		ops = append(ops, out.Ops...)
	}
	unlock, err := p.Lock()
	if err != nil {
		return total, err
	}
	defer unlock()
	if rec, err = p.LoadRecord(); err != nil {
		return total, err
	}
	var kept []Op
	for _, o := range ops {
		if o.Op == "add" {
			continue // dreaming tidies; it does not decide
		}
		if _, ok := rec.Entries[strings.TrimSpace(o.Key)]; !ok {
			total.Skipped++
			continue
		}
		if o.Op == "drop" && strings.TrimSpace(o.Reason) == "" {
			total.Skipped++
			continue
		}
		if o.Op == "replace" && len(o.Cites) == 0 {
			o.Cites = rec.Entries[strings.TrimSpace(o.Key)].Cites
		}
		kept = append(kept, o)
	}
	r := rec.Apply(kept, now, nil, "dream")
	total.Replaced, total.Dropped = r.Replaced, r.Dropped
	total.Skipped += r.Skipped
	if err := p.SaveRecord(rec); err != nil {
		return total, err
	}
	state, err := p.LoadState()
	if err != nil {
		return total, err
	}
	state.LastDream = now.UTC()
	state.SessionsSince = map[string]bool{}
	return total, p.SaveState(state)
}

// dreamBatches renders the record oldest first, grouped by kind so related
// entries share a batch, cut into pieces of about n characters.
func dreamBatches(r Record, n int) []string {
	var out []string
	var b strings.Builder
	for _, k := range Kinds {
		keys := r.keysOf(k.Kind)
		sort.SliceStable(keys, func(i, j int) bool { return r.Entries[keys[i]].Date < r.Entries[keys[j]].Date })
		for _, key := range keys {
			line := fmt.Sprintf("- [%s] %s\n", k.Kind, RenderEntry(key, r.Entries[key]))
			if b.Len() > 0 && b.Len()+len(line) > n {
				out = append(out, b.String())
				b.Reset()
			}
			b.WriteString(line)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
