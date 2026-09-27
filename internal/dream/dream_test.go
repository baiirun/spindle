package dream

import (
	"testing"
	"time"

	"spindle/internal/episode"
)

func TestBatchesNeverSpanACut(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 7, 1, h, 0, 0, 0, time.UTC) }
	var eps []episode.Episode
	for h := 1; h <= 7; h++ {
		eps = append(eps, episode.Episode{Chunk: h, End: at(h)})
	}
	// Cut at 04:30: episodes ending 1..4 must be folded before it, 5.. after.
	got := batches(eps, 3, []time.Time{at(4).Add(30 * time.Minute)})
	var sizes []int
	for _, b := range got {
		sizes = append(sizes, len(b))
	}
	want := []int{3, 1, 3}
	if len(sizes) != len(want) {
		t.Fatalf("batch sizes %v, want %v", sizes, want)
	}
	for i := range want {
		if sizes[i] != want[i] {
			t.Fatalf("batch sizes %v, want %v", sizes, want)
		}
	}
	steps := []Step{{N: 1, AsOf: at(3)}, {N: 2, AsOf: at(4)}, {N: 3, AsOf: at(7)}}
	if s, ok := AsOf(steps, at(4).Add(30*time.Minute)); !ok || s.N != 2 {
		t.Fatalf("AsOf picked step %d, want 2", s.N)
	}
}

func TestApplyLogsEveryRuleChangeAndCapsThreads(t *testing.T) {
	s := newState()
	s.Apply([]Op{{Op: "set_rule", Key: "stress die", Text: "every 1 adds stress", Who: "user"}}, 1, "2026-07-07")
	s.Apply([]Op{{Op: "set_rule", Key: "stress die", Text: "any 1 adds one stress", Who: "user", Why: "simpler"}}, 2, "2026-07-08")
	s.Apply([]Op{{Op: "remove_rule", Key: "stress die", Reason: "replaced by push roll"}}, 3, "2026-07-09")
	if len(s.Rules) != 0 || len(s.Changes) != 2 {
		t.Fatalf("rules %d, changes %d; want 0 rules and 2 logged changes", len(s.Rules), len(s.Changes))
	}
	if s.Changes[0].Old != "every 1 adds stress" || s.Changes[1].New != "(removed)" {
		t.Fatalf("changes not logged with old text: %+v", s.Changes)
	}
	var ops []Op
	for i := 0; i < maxThreads+3; i++ {
		ops = append(ops, Op{Op: "upsert_thread", Key: string(rune('a' + i)), Status: "active", Now: "x"})
	}
	s.Apply(ops, 4, "2026-07-10")
	if len(s.Threads) != maxThreads {
		t.Fatalf("threads %d, want cap %d", len(s.Threads), maxThreads)
	}
}
