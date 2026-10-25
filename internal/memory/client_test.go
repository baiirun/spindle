package memory

import (
	"strings"
	"testing"
)

func TestClient(t *testing.T) {
	p := project(t)
	cli, agent := p.Client("cli"), p.Client("codex")
	if msg, err := cli.Write(Op{Name: "Go Vet", Type: "directive", Description: "Run go vet before committing", Body: "Run it."}); err != nil || msg != "Saved go-vet.md." {
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
