package store

import (
	"path/filepath"
	"testing"
)

func TestResolveUsesAbsoluteSpindleHome(t *testing.T) {
	t.Setenv(HomeEnv, "/tmp/spindle-store")
	roots, err := Resolve("episodes-v2")
	if err != nil {
		t.Fatal(err)
	}
	if roots.Corpus != "/tmp/spindle-store/corpus" || roots.Episodes != "/tmp/spindle-store/episodes/episodes-v2" {
		t.Fatalf("roots = %#v", roots)
	}
}

func TestResolveRejectsRelativeSpindleHome(t *testing.T) {
	t.Setenv(HomeEnv, filepath.Join("relative", "spindle"))
	if _, err := Resolve("episodes-v2"); err == nil {
		t.Fatal("Resolve accepted a relative SPINDLE_HOME")
	}
}
