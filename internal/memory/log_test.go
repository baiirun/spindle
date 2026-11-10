package memory

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// Gists are checked once, at the boundary, and never clipped.
func TestParseGist(t *testing.T) {
	t.Skip("pending: ParseGist")
	exactly := strings.Repeat("é", GistBytes/2) // 2 bytes each: exactly GistBytes
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"Rollups are written by a side model.", "Rollups are written by a side model.", true},
		{"  two\n lines\tcollapse  ", "two lines collapse", true},
		{exactly, exactly, true},
		{exactly + "x", "", false},
		{" \n\t ", "", false},
	} {
		got, err := ParseGist(c.in)
		if (err == nil) != c.ok || string(got) != c.want {
			t.Errorf("ParseGist(%q) = %q, %v; want %q, ok %v", c.in, got, err, c.want, c.ok)
		}
	}
}

// The log is the truth: a crash's torn tail is tolerated, damage anywhere
// else is reported rather than skipped.
func TestEntriesTornTailAndCorruption(t *testing.T) {
	t.Skip("pending: Append, Entries")
	p := project(t)
	if err := p.Append([]Entry{entry(t, "e-1", "s", t0, "first"), entry(t, "e-2", "s", t0, "second")}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(p.logPath(), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"id":"e-3","at":"2026-10`)
	f.Close()
	if es, err := p.Entries(); err != nil || len(es) != 2 {
		t.Fatalf("torn tail: %d entries, %v; want 2, nil", len(es), err)
	}

	b, _ := os.ReadFile(p.logPath())
	lines := strings.SplitN(string(b), "\n", 2)
	os.WriteFile(p.logPath(), []byte("not json\n"+lines[1]), 0o644)
	if _, err := p.Entries(); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("corrupt first line: err %v, want one naming line 1", err)
	}
}

// Concurrent appenders (the worker, an MCP call, the CLI) lose nothing and
// never interleave inside a batch.
func TestAppendKeepsBatchesWhole(t *testing.T) {
	t.Skip("pending: Append, Entries")
	p := project(t)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range 10 {
				batch := []Entry{
					entry(t, fmt.Sprintf("e-%d-%d-a", w, b), "s", t0, "a"),
					entry(t, fmt.Sprintf("e-%d-%d-b", w, b), "s", t0, "b"),
				}
				if err := p.Append(batch); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	es, err := p.Entries()
	if err != nil || len(es) != 160 {
		t.Fatalf("%d entries, %v; want 160", len(es), err)
	}
	for i := 0; i < len(es); i += 2 {
		if a, b := string(es[i].ID), string(es[i+1].ID); !strings.HasSuffix(a, "-a") || b != strings.TrimSuffix(a, "a")+"b" {
			t.Fatalf("batch split at %d: %s then %s", i, a, b)
		}
	}
}
