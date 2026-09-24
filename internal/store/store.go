// Package store resolves Spindle's durable data locations.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HomeEnv selects Spindle's durable data root.
const HomeEnv = "SPINDLE_HOME"

// Roots are the durable corpus and derived episode locations for one episode
// format version. They are independent of the caller's working directory.
type Roots struct {
	Home     string
	Corpus   string
	Episodes string
}

// Resolve returns global durable locations. SPINDLE_HOME must be absolute so
// an installed CLI behaves the same from every working directory.
func Resolve(episodeVersion string) (Roots, error) {
	home := strings.TrimSpace(os.Getenv(HomeEnv))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Roots{}, fmt.Errorf("resolve %s: %w", HomeEnv, err)
		}
		home = filepath.Join(userHome, ".spindle")
	}
	if !filepath.IsAbs(home) {
		return Roots{}, fmt.Errorf("%s must be an absolute path", HomeEnv)
	}
	home = filepath.Clean(home)
	return Roots{
		Home:     home,
		Corpus:   filepath.Join(home, "corpus"),
		Episodes: filepath.Join(home, "episodes", episodeVersion),
	}, nil
}
