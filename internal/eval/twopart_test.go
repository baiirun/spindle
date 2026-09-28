package eval

import (
	"strings"
	"testing"
)

func TestLedgerApply(t *testing.T) {
	valid := map[string]bool{"codex:aaaaaaaa#L1": true, "codex:aaaaaaaa#L2": true}
	l := Ledger{Entries: map[string]LedgerEntry{}}
	l.apply([]ledgerOp{
		{Op: "add", Kind: "decision", Key: "dice", Text: "d6 damage", Who: "user", Cites: []string{"codex:aaaaaaaa#L1", "codex:bogus#L9"}},
		{Op: "add", Kind: "tried", Key: "grid", Text: "hex grid", Why: "too fiddly"},
		{Op: "drop", Key: "missing"},
	}, 1, "2026-07-01", valid)
	if l.BadCites != 1 || l.Skipped != 1 || len(l.Entries) != 2 {
		t.Fatalf("after add: bad=%d skipped=%d entries=%d", l.BadCites, l.Skipped, len(l.Entries))
	}
	l.apply([]ledgerOp{
		{Op: "replace", Kind: "decision", Key: "dice", Text: "flat damage", Why: "dice dropped", Date: "2026-07-24", Cites: []string{"codex:aaaaaaaa#L2"}},
		{Op: "drop", Key: "grid", Reason: "no longer relevant"},
	}, 2, "2026-07-24", valid)
	e := l.Entries["dice"]
	if e.Text != "flat damage" || !strings.Contains(e.Replaced, "d6 damage") {
		t.Fatalf("replace did not keep the old text: %+v", e)
	}
	if len(l.Log) != 2 || l.Log[0].Op != "replace" || l.Log[1].Op != "drop" {
		t.Fatalf("log: %+v", l.Log)
	}
	if !strings.Contains(l.Render(), "Replaced: \"d6 damage\"") {
		t.Fatalf("render lacks replaced text:\n%s", l.Render())
	}
}

func TestLedgerHardCap(t *testing.T) {
	l := Ledger{Entries: map[string]LedgerEntry{}}
	var ops []ledgerOp
	for i := 0; i < 80; i++ {
		ops = append(ops, ledgerOp{Op: "add", Kind: "decision", Key: strings.Repeat("k", i+1), Text: strings.Repeat("x", 390)})
	}
	l.apply(ops, 1, "2026-07-01", nil)
	if n := len(l.Render()); n > ledgerHardCap {
		t.Fatalf("rendered %d chars over the cap", n)
	}
	if l.Log[len(l.Log)-1].Op != "evict" {
		t.Fatalf("expected evictions logged")
	}
}
