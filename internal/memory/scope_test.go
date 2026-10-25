package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestGlobalScope(t *testing.T) {
	a := project(t)
	b := At(a.Home, t.TempDir()) // another project, same home
	c := a.Client("cli")
	// A user memory goes global by default; a directive stays in the project
	// unless scoped global.
	if msg, _ := c.Write(Op{Name: "role", Type: "user", Description: "Byron, solo dev", Body: "Go."}); msg != "Saved global/role.md." {
		t.Errorf("user write: %q", msg)
	}
	c.Write(Op{Name: "no-attribution", Type: "directive", Description: "No Claude attribution in commits or PRs", Body: "Never.", Scope: "global"})
	c.Write(Op{Name: "go-vet", Type: "directive", Description: "Run go vet before committing", Body: "Run it."})
	// Updating a global memory by bare name updates it in place.
	c.Write(Op{Name: "role", Type: "user", Description: "Byron, solo dev on spindle", Body: "Go and TS."})
	if _, ok := a.Get("role"); ok {
		t.Error("role leaked into the project store")
	}

	// Project b sees the global memories but not a's.
	sb := b.StartContext()
	if !strings.Contains(sb, "[no-attribution](global/no-attribution.md)") || !strings.Contains(sb, "solo dev on spindle") || strings.Contains(sb, "go-vet") {
		t.Errorf("b start context:\n%s", sb)
	}
	hits, total, _ := b.Client("cli").Search("attribution commits", 0)
	if len(hits) != 1 || hits[0].Name != "global/no-attribution" || total != 2 {
		t.Errorf("search from b: %+v of %d", hits, total)
	}

	// Recall from b can pick and attach a global memory.
	id, _ := b.StartRecall("s", "claude", "add claude attribution to commits?", time.Now())
	b.RunSelect(context.Background(), "s", id, nil, true)
	if text, _ := b.WaitRecall("s", id, 0); !strings.Contains(text, "Memory: global/no-attribution.md") {
		t.Errorf("recall: %q", text)
	}

	// Read, history and delete by global/ name.
	if m, err := b.Client("cli").Read("global/role"); err != nil || !strings.Contains(m.Body, "TS") {
		t.Errorf("read: %+v %v", m, err)
	}
	if h, _ := c.History("global/role"); len(h) != 2 {
		t.Errorf("history %d", len(h))
	}
	if msg, err := c.Delete("global/role", "test"); err != nil || msg != "Deleted global/role.md." {
		t.Errorf("delete: %q %v", msg, err)
	}
	if s, _ := b.Client("cli").Status(); s.Global != 1 || s.Total != 0 {
		t.Errorf("status %+v", s)
	}
}
