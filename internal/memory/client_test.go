package memory

import (
	"context"
	"strings"
	"testing"
)

func TestClient(t *testing.T) {
	p := project(t)
	cli, agent := p.Client("cli"), p.Client("codex")
	if msg, err := cli.Write(Op{Name: "Go Vet", Type: "directive", Description: "Run go vet before committing", Body: "Run it."}); err != nil || msg != "Saved proj/go-vet.md." {
		t.Fatalf("write: %q %v", msg, err)
	}
	// An edit can keep the type and description.
	if _, err := agent.Write(Op{Name: "go-vet", Body: "Run go vet ./... and fix what it reports."}); err != nil {
		t.Fatal(err)
	}
	m, err := cli.Read("go-vet")
	if err != nil || m.Type != "directive" || !strings.Contains(m.Body, "fix what it reports") {
		t.Fatalf("read: %+v %v", m, err)
	}
	if hits, total, _ := cli.Search("vet", 0); len(hits) != 1 || total != 1 {
		t.Errorf("search: %d of %d", len(hits), total)
	}
	if s, _ := cli.Status(); s.Total != 1 || s.Types["directive"] != 1 {
		t.Errorf("status: %+v", s)
	}
	if !strings.Contains(cli.Index(), "go-vet.md") {
		t.Error("index lacks go-vet")
	}
	if _, err := cli.Delete("go-vet", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Read("go-vet"); err == nil {
		t.Error("read after delete: want error")
	}
	// Only the agent's own writes make extraction stand down.
	changes, _ := p.Changes()
	var by []string
	for _, c := range changes {
		by = append(by, c.By)
	}
	if strings.Join(by, ",") != "cli,agent,cli" {
		t.Errorf("changes by %v", by)
	}
	evs, _ := p.Events()
	var calls []string
	for _, e := range evs {
		calls = append(calls, e.Agent+":"+e.Name)
	}
	want := "cli:write,codex:write,cli:read,cli:search,cli:index,cli:delete,cli:read"
	if strings.Join(calls, ",") != want {
		t.Errorf("events %v, want %s", calls, want)
	}
}

func TestHistoryRestore(t *testing.T) {
	p := project(t)
	c := p.Client("cli")
	c.Write(Op{Name: "rule", Type: "directive", Description: "First", Body: "Version one."})
	c.Write(Op{Name: "rule", Body: "Version two."})
	c.Delete("rule", "wrong")
	hist, err := c.History("rule")
	if err != nil || len(hist) != 3 || hist[0].Op != "delete" || hist[2].Old != "" {
		t.Fatalf("history %+v %v", hist, err)
	}
	if out := FormatHistory("rule", hist, true); !strings.Contains(out, "1. ") || !strings.Contains(out, "deleted by cli: wrong") || !strings.Contains(out, "| Version two.") {
		t.Errorf("format:\n%s", out)
	}
	if _, err := c.Restore("rule", 3); err == nil {
		t.Error("restore before creation: want error")
	}
	if _, err := c.Restore("rule", 2); err != nil { // the version two replaced
		t.Fatal(err)
	}
	if m, err := c.Read("rule"); err != nil || strings.TrimSpace(m.Body) != "Version one." || m.Description != "First" {
		t.Errorf("restored %+v %v", m, err)
	}
	evs, _ := c.Activity(LogFilter{Kind: "call", Last: 2})
	if len(evs) != 2 || evs[1].Name != "read" {
		t.Errorf("activity %+v", evs)
	}
}

func TestOneDreamAtATime(t *testing.T) {
	p := project(t)
	p.Init()
	unlock, err := lockFile(p.Dir+"/dream.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := p.Client("cli").Dream(context.Background(), nil, true); err != ErrDreaming {
		t.Errorf("err %v, want ErrDreaming", err)
	}
}

func TestEmptyIndexSaysSo(t *testing.T) {
	p := project(t)
	if got := p.Client("cli").Index(); !strings.Contains(got, "# proj (this folder's project)\n\nNo memories yet.") {
		t.Errorf("index %q", got)
	}
}
