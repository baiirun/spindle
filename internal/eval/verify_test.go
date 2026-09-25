package eval

import (
	"reflect"
	"testing"

	"spindle/internal/corpus"
)

func TestCiteResolverOnlyAcceptsPreCutoffItems(t *testing.T) {
	before := []corpus.Item{{ID: "codex:019e85fe#L100"}, {ID: "codex:019e85fe#L205"}, {ID: "claude:226b239e#L205"}}
	resolve := citeResolver(before)
	got := resolve([]string{
		"codex:019e85fe#L100", // exact
		"L100",                // shortened, unique line
		"#L205",               // shortened, ambiguous across sessions: rejected
		"codex:019e85fe#L999", // not a pre-cutoff item: rejected
		"[claude:226b239e#L205]",
	})
	want := []string{"codex:019e85fe#L100", "codex:019e85fe#L100", "claude:226b239e#L205"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolve = %v, want %v", got, want)
	}
}
