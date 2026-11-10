package memory

import (
	"slices"
	"testing"
	"time"
)

// rollAll summarizes every window of es bottom up, as the worker would
// once all of them have closed, and returns the rollups.
func rollAll(t *testing.T, es []Entry, now time.Time) []Rollup {
	t.Helper()
	var rs []Rollup
	for range 4 { // sessions, days, weeks, then nothing is left
		tl := Timeline(es, rs, time.UTC)
		due := DueRollups(tl, now.Add(365*24*time.Hour), time.UTC)
		for _, task := range due {
			rs = append(rs, Rollup{Window: task.Window, Line: Gist("summary of " + task.Window.String()), Inputs: task.Node.Digest, Built: now})
		}
	}
	return rs
}

func windows(tasks []RollupTask) []string {
	var out []string
	for _, t := range tasks {
		out = append(out, t.Window.String())
	}
	return out
}

// What comes due, and when: closed windows whose children are all
// summarized, children first.
func TestDueRollups(t *testing.T) {
	loc := time.UTC
	mon := time.Date(2026, 11, 9, 10, 0, 0, 0, loc) // a Monday, ISO week 46
	es := []Entry{
		entry(t, "e-1", "a", mon, "one"),
		entry(t, "e-2", "a", mon.Add(5*time.Minute), "two"),
	}
	tl := Timeline(es, nil, loc)
	for _, c := range []struct {
		name string
		now  time.Time
		want []string
	}{
		{"session still going", mon.Add(5*time.Minute + SessionIdle - time.Second), nil},
		{"session idle", mon.Add(5*time.Minute + SessionIdle), []string{"s:a"}},
		{"day over, session not yet summarized", mon.Add(24 * time.Hour), []string{"s:a"}},
	} {
		if got := windows(DueRollups(tl, c.now, loc)); !slices.Equal(got, c.want) {
			t.Errorf("%s: due %v, want %v", c.name, got, c.want)
		}
	}

	// Once the session has its rollup, the closed day comes due; the week
	// waits for the day.
	all := rollAll(t, es, mon)
	rolled := Timeline(es, all[:1], loc) // the session's: rollAll writes it first
	if got := windows(DueRollups(rolled, mon.Add(24*time.Hour), loc)); !slices.Equal(got, []string{"d:2026-11-09"}) {
		t.Errorf("after the session's rollup: due %v, want the day", got)
	}

	// An entry added to a summarized session makes it, its day and its week
	// stale, and they come due again bottom up.
	later := append(es, entry(t, "e-3", "a", mon.Add(time.Hour), "three"))
	stale := Timeline(later, all, loc)
	if stale[0].Line != "" || stale[0].Children[0].Line != "" || stale[0].Children[0].Children[0].Line != "" {
		t.Errorf("stale rollups still shown: week %q day %q session %q", stale[0].Line, stale[0].Children[0].Line, stale[0].Children[0].Children[0].Line)
	}
	if got := windows(DueRollups(stale, mon.Add(14*24*time.Hour), loc)); !slices.Equal(got, []string{"s:a"}) {
		t.Errorf("after a new entry: due %v, want the session first", got)
	}
}
