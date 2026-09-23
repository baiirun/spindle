// Package wake provides read-only retrieval over episodes and normalized
// transcript chunks. It deliberately keeps indexing simple for the first
// continuity trial: lexical search plus evidence-preserving reads.
package wake

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"spindle/internal/episode"
)

// Result is one discoverable source. Ref is safe to pass back to Read or
// Related; it is not a filesystem path supplied by an agent.
type Result struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
	Score   int    `json:"score"`
}

// Options selects the two rebuildable stores available to retrieval.
type Options struct {
	CorpusRoot  string
	EpisodeRoot string
	Scope       string
	Query       string
	Limit       int
}

// Search returns lexical matches from both derived episodes and raw sources.
// Episodes receive a small score boost so wake starts with compact context and
// only expands raw transcript evidence when that context is insufficient.
func Search(o Options) ([]Result, error) {
	if strings.TrimSpace(o.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if o.Limit <= 0 {
		o.Limit = 8
	}
	terms := terms(o.Query)
	var results []Result
	if o.EpisodeRoot != "" {
		paths, err := filepath.Glob(filepath.Join(o.EpisodeRoot, "*", "*", "*.md"))
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			text, err := os.ReadFile(path)
			if err != nil || !matchesScope(string(text), o.Scope) {
				continue
			}
			score := score(string(text), terms) + 1
			if score == 1 {
				continue
			}
			e, err := episode.ReadPath(path)
			if err != nil {
				continue
			}
			results = append(results, Result{
				Ref: fmt.Sprintf("episode:%s/%s/%04d", e.Source, e.Session, e.Chunk), Kind: "episode",
				Title: e.Title, Excerpt: excerpt(string(text), terms), Score: score,
			})
		}
	}
	if o.CorpusRoot != "" {
		paths, err := filepath.Glob(filepath.Join(o.CorpusRoot, "*", "*", "*.md"))
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			text, err := os.ReadFile(path)
			if err != nil || !matchesScope(string(text), o.Scope) {
				continue
			}
			score := score(string(text), terms)
			if score == 0 {
				continue
			}
			source, session, chunk, ok := sourceParts(path)
			if !ok {
				continue
			}
			results = append(results, Result{
				Ref: fmt.Sprintf("transcript:%s/%s/%04d", source, session, chunk), Kind: "transcript",
				Title:   fmt.Sprintf("%s session %s, chunk %d", source, short(session), chunk),
				Excerpt: excerpt(string(text), terms), Score: score,
			})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Ref < results[j].Ref
	})
	if len(results) > o.Limit {
		results = results[:o.Limit]
	}
	return results, nil
}

// Wake favors compact episodes over raw transcript matches. It is the normal
// entry point for an agent that needs orientation before drilling into proof.
func Wake(o Options) ([]Result, error) {
	limit := o.Limit
	if limit <= 0 {
		limit = 8
	}
	// Search must consider more than the caller-facing limit before this method
	// can prioritize compact episodes over frequent raw-term matches.
	o.Limit = 10000
	results, err := Search(o)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Kind != results[j].Kind {
			return results[i].Kind == "episode"
		}
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Ref < results[j].Ref
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// Read expands a source selected by Search. It rejects path traversal and
// only permits references within the configured source roots.
func Read(o Options, ref string) (string, error) {
	kind, source, session, chunk, err := parseRef(ref)
	if err != nil {
		return "", err
	}
	var root string
	switch kind {
	case "episode":
		root = o.EpisodeRoot
	case "transcript":
		root = o.CorpusRoot
	default:
		return "", fmt.Errorf("unknown ref kind %q", kind)
	}
	if root == "" {
		return "", fmt.Errorf("%s retrieval is not configured", kind)
	}
	path := filepath.Join(root, source, session, fmt.Sprintf("%04d.md", chunk))
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil || !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	b, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Related follows explicit continuation links from an episode. It does not
// invent semantic relationships; those can be added after the trial proves a
// need for them.
func Related(o Options, ref string) ([]episode.Link, error) {
	kind, source, session, chunk, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	if kind != "episode" {
		return nil, fmt.Errorf("related only supports episode references")
	}
	e, err := episode.ReadPath(episode.Path(o.EpisodeRoot, source, session, chunk))
	if err != nil {
		return nil, err
	}
	return e.Continues, nil
}

func terms(query string) []string {
	seen := map[string]bool{}
	var out []string
	for _, term := range strings.Fields(strings.ToLower(query)) {
		term = strings.Trim(term, ".,:;!?()[]{}\"'")
		if len(term) > 1 && !seen[term] {
			seen[term] = true
			out = append(out, term)
		}
	}
	return out
}

func score(text string, terms []string) int {
	lower := strings.ToLower(text)
	var n int
	for _, term := range terms {
		n += strings.Count(lower, term)
	}
	return n
}

func matchesScope(text, scope string) bool {
	return scope == "" || strings.Contains(strings.ToLower(text), strings.ToLower(scope))
}

func excerpt(text string, terms []string) string {
	lower := strings.ToLower(text)
	pos := -1
	for _, term := range terms {
		if i := strings.Index(lower, term); i >= 0 && (pos < 0 || i < pos) {
			pos = i
		}
	}
	if pos < 0 {
		return ""
	}
	start, end := pos-180, pos+420
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	out := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

func parseRef(ref string) (kind, source, session string, chunk int, err error) {
	kind, path, ok := strings.Cut(ref, ":")
	if !ok || (kind != "episode" && kind != "transcript") {
		err = fmt.Errorf("invalid ref %q", ref)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		err = fmt.Errorf("invalid ref %q", ref)
		return
	}
	if _, scanErr := fmt.Sscanf(parts[2], "%d", &chunk); scanErr != nil || chunk < 1 {
		err = fmt.Errorf("invalid ref %q", ref)
		return
	}
	source, session = parts[0], parts[1]
	if strings.ContainsAny(source+session, `\\`) || strings.Contains(source+session, "..") {
		err = fmt.Errorf("invalid ref %q", ref)
	}
	return
}

func sourceParts(path string) (source, session string, chunk int, ok bool) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < 3 {
		return "", "", 0, false
	}
	source, session = parts[len(parts)-3], parts[len(parts)-2]
	_, err := fmt.Sscanf(strings.TrimSuffix(parts[len(parts)-1], ".md"), "%d", &chunk)
	return source, session, chunk, err == nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
