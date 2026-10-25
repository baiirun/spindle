package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestEventsTraceRecall(t *testing.T) {
	p := project(t)
	t0 := time.Now()
	p.StartRecall("s", "claude", "thanks", t0)
	write(t, p, Op{Op: "write", Name: "event-store", Type: "project", Description: "Events are stored in an append-only log", Body: "In events.jsonl."})
	id, _ := p.StartRecall("s", "claude", "where are events stored?", t0)
	p.RunSelect(context.Background(), "s", id, nil, true)
	if text, _ := p.WaitRecall("s", id, 0); text == "" {
		t.Fatal("no recall")
	}
	evs, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range evs {
		got = append(got, e.Kind+":"+strings.Join(e.Files, ","))
	}
	want := "skip:|select:event-store.md|recall:event-store.md"
	if strings.Join(got, "|") != want {
		t.Fatalf("events %v, want %s", got, want)
	}
	if e := evs[2]; e.Agent != "claude" || e.Note != "with the prompt" {
		t.Errorf("recall event %+v", e)
	}
	if line := evs[1].String(); !strings.Contains(line, `"where are events stored?" → event-store.md`) {
		t.Errorf("select line %q", line)
	}
}
