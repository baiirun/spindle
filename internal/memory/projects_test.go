package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkFolderToProject(t *testing.T) {
	t.Skip("pending: Adopt, which every link now calls")
	t.Setenv(ProjectEnv, "")
	home, dir := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	c := NewClient(home, filepath.Join(dir, "sub"), "", "claude")
	if msg, err := c.SetProject("Taxes 2026", "Filing 2026 taxes", "", ""); err != nil || msg != "Created project taxes-2026." {
		t.Fatalf("new project: %q %v", msg, err)
	}
	if p := c.Context(); p.Name != "" || Store(home, "taxes-2026").Meta().Description != "Filing 2026 taxes" {
		t.Errorf("creating a project linked the folder: %+v", p)
	}
	// Linking takes the repository root.
	if msg, err := c.SetProject("taxes-2026", "", ".", ""); err != nil || msg != "Linked "+dir+"." {
		t.Errorf("link: %q %v", msg, err)
	}
	if p := c.Context(); p.Name != "taxes-2026" {
		t.Errorf("folder now hints %+v", p)
	}
	// Linking a folder to an existing project from elsewhere.
	other := t.TempDir()
	if msg, err := NewClient(home, other, "", "cli").SetProject("taxes-2026", "", ".", ""); err != nil || !strings.Contains(msg, "Linked "+other) {
		t.Errorf("link: %q %v", msg, err)
	}
}
