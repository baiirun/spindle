// Package wake provides read-only retrieval over episodes and normalized
// transcript chunks. It deliberately keeps indexing simple for the first
// continuity trial: lexical search plus evidence-preserving reads.
package wake

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"spindle/internal/corpus"
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

// ResumeResult is the compact startup packet for a known prior session.
// Latest is nil only when every recorded range is source-only.
type ResumeResult struct {
	Latest     *Result        `json:"latest,omitempty"`
	Continues  []episode.Link `json:"continues,omitempty"`
	SourceOnly []Result       `json:"source_only,omitempty"`
	// Projected and Captured bound the handoff: the latest usable episode
	// covers the session through Projected, while the corpus holds it through
	// Captured. SourceOnly lists every range in between that must be read raw.
	Projected *Mark `json:"projected_through,omitempty"`
	Captured  *Mark `json:"captured_through,omitempty"`
}

// Mark is a position in a source session: a chunk and the time of its last item.
type Mark struct {
	Chunk int       `json:"chunk"`
	End   time.Time `json:"end"`
}

// Options selects the two rebuildable stores available to retrieval.
type Options struct {
	CorpusRoot  string
	EpisodeRoot string
	Source      string
	Session     string
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
	if o.Session != "" && o.Source == "" {
		return nil, fmt.Errorf("session filter requires a source filter")
	}
	if (o.Source != "" && !safePart(o.Source)) || (o.Session != "" && !safePart(o.Session)) {
		return nil, fmt.Errorf("invalid source or session filter")
	}
	if o.Limit <= 0 {
		o.Limit = 8
	}
	terms := terms(o.Query)
	var results []Result
	if o.EpisodeRoot != "" {
		paths, err := searchPaths(o.EpisodeRoot, o.Source, o.Session)
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
		paths, err := searchPaths(o.CorpusRoot, o.Source, o.Session)
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

func searchPaths(root, source, session string) ([]string, error) {
	parts := []string{root}
	if source == "" {
		parts = append(parts, "*", "*", "*.md")
	} else if session == "" {
		parts = append(parts, source, "*", "*.md")
	} else {
		parts = append(parts, source, session, "*.md")
	}
	return filepath.Glob(filepath.Join(parts...))
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

// Resume returns the latest usable episode for a known session, its immediate
// carried context, and any newer source-only ranges that must be expanded from
// raw transcript. When the session has not been projected yet, it returns all
// of its raw chunks as explicit source-only ranges. It is the warm-start
// counterpart to query-based Wake.
func Resume(o Options, source, session string) (ResumeResult, error) {
	if o.EpisodeRoot == "" {
		return ResumeResult{}, fmt.Errorf("episode retrieval is not configured")
	}
	if !safePart(source) || !safePart(session) {
		return ResumeResult{}, fmt.Errorf("invalid source or session")
	}
	episodes, err := episode.ReadSession(o.EpisodeRoot, source, session)
	if err != nil {
		return ResumeResult{}, err
	}
	if len(episodes) == 0 {
		return unprojectedSession(o.CorpusRoot, source, session)
	}
	result := ResumeResult{}
	latest := -1
	for i := len(episodes) - 1; i >= 0; i-- {
		if episodes[i].Status != episode.StatusSourceOnly {
			latest = i
			break
		}
	}
	if latest >= 0 {
		e := episodes[latest]
		result.Latest = &Result{Ref: fmt.Sprintf("episode:%s/%s/%04d", e.Source, e.Session, e.Chunk), Kind: "episode", Title: e.Title}
		result.Continues = e.Continues
		result.Projected = &Mark{Chunk: e.Chunk, End: e.End}
	} else {
		result.Continues = episodes[len(episodes)-1].Continues
	}
	projectedChunk := 0
	if latest >= 0 {
		projectedChunk = episodes[latest].Chunk
	}
	if o.CorpusRoot == "" {
		// Without the corpus, only recorded source-only episodes can be listed.
		for _, e := range episodes[latest+1:] {
			result.SourceOnly = append(result.SourceOnly, rawRange(e.Source, e.Session, e.Chunk, e.Title, "Source-only range; expand before relying on this handoff."))
		}
		return result, nil
	}
	return withRawTail(result, o.CorpusRoot, source, session, episodes, projectedChunk)
}

// withRawTail compares the corpus with the recorded episodes. Every chunk from
// the latest usable episode onward that lacks a current summary goes into the
// raw tail: source-only projections, chunks never projected, and chunks whose
// content changed after projection (typically a final chunk that kept growing).
func withRawTail(result ResumeResult, corpusRoot, source, session string, episodes []episode.Episode, projectedChunk int) (ResumeResult, error) {
	paths, err := filepath.Glob(filepath.Join(corpusRoot, source, session, "*.md"))
	if err != nil {
		return result, err
	}
	sort.Strings(paths)
	byChunk := map[int]episode.Episode{}
	for _, e := range episodes {
		byChunk[e.Chunk] = e
	}
	for _, p := range paths {
		h, err := corpus.ReadChunkHeader(p)
		if err != nil {
			return result, err
		}
		if result.Captured == nil || h.Index > result.Captured.Chunk {
			result.Captured = &Mark{Chunk: h.Index, End: h.End}
		}
		if h.Index < projectedChunk {
			continue
		}
		e, projected := byChunk[h.Index]
		raw, err := os.ReadFile(p)
		if err != nil {
			return result, err
		}
		changed := projected && e.SourceHash != "" && e.SourceHash != sourceDigest(raw)
		switch {
		case h.Index == projectedChunk && !changed:
			continue // covered by the latest usable episode
		case changed:
			result.SourceOnly = append(result.SourceOnly, rawRange(source, session, h.Index, e.Title,
				fmt.Sprintf("Changed since projection: the episode covers through %s; read the raw range for anything later.", e.End.UTC().Format(time.RFC3339))))
		case !projected:
			result.SourceOnly = append(result.SourceOnly, rawRange(source, session, h.Index, fmt.Sprintf("Unprojected raw range %d", h.Index),
				"Not yet projected; expand this raw range before relying on the handoff."))
		case e.Status == episode.StatusSourceOnly:
			result.SourceOnly = append(result.SourceOnly, rawRange(source, session, h.Index, e.Title,
				"Source-only range; expand before relying on this handoff."))
		}
	}
	return result, nil
}

func rawRange(source, session string, chunk int, title, why string) Result {
	return Result{Ref: fmt.Sprintf("transcript:%s/%s/%04d", source, session, chunk), Kind: "transcript", Title: title, Excerpt: why}
}

// sourceDigest matches the hash episode projection stores for its source chunk.
func sourceDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func unprojectedSession(corpusRoot, source, session string) (ResumeResult, error) {
	if corpusRoot == "" {
		return ResumeResult{}, fmt.Errorf("session %s:%s has no episodes and transcript retrieval is not configured", source, session)
	}
	chunks, err := corpus.ReadSession(corpusRoot, source, session)
	if err != nil {
		return ResumeResult{}, err
	}
	if len(chunks) == 0 {
		return ResumeResult{}, fmt.Errorf("session %s:%s was not found in episodes or corpus", source, session)
	}
	result := ResumeResult{SourceOnly: make([]Result, 0, len(chunks))}
	last := chunks[len(chunks)-1]
	result.Captured = &Mark{Chunk: last.Index, End: last.End}
	for _, chunk := range chunks {
		result.SourceOnly = append(result.SourceOnly, Result{
			Ref:     fmt.Sprintf("transcript:%s/%s/%04d", source, session, chunk.Index),
			Kind:    "transcript",
			Title:   fmt.Sprintf("Unprojected raw range %d of %d", chunk.Index, len(chunks)),
			Excerpt: "No episode was recorded; expand this raw range before relying on the handoff.",
		})
	}
	return result, nil
}

// Read expands a source selected by Search. It rejects path traversal and
// only permits references within the configured source roots.
func Read(o Options, ref string) (string, error) {
	if source, sessionPrefix, line, ok := parseEvidenceRef(ref); ok {
		return readEvidence(o.CorpusRoot, source, sessionPrefix, line, ref)
	}
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

// readEvidence expands one stable transcript item citation. Episode citations
// use an eight-character session prefix, so resolve it against corpus sessions
// and reject an ambiguous prefix rather than guessing.
func readEvidence(root, source, sessionPrefix string, line int, ref string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("transcript retrieval is not configured")
	}
	if !safePart(source) || !safePart(sessionPrefix) {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	entries, err := os.ReadDir(filepath.Join(root, source))
	if err != nil {
		return "", err
	}
	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), sessionPrefix) {
			sessions = append(sessions, entry.Name())
		}
	}
	if len(sessions) != 1 {
		return "", fmt.Errorf("evidence ref %q matches %d sessions", ref, len(sessions))
	}
	chunks, err := corpus.ReadSession(root, source, sessions[0])
	if err != nil {
		return "", err
	}
	for _, chunk := range chunks {
		for _, item := range chunk.Items {
			if item.ID == ref && item.Line == line {
				return item.Render(), nil
			}
		}
	}
	return "", fmt.Errorf("evidence ref %q was not found", ref)
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
	if !safePart(source) || !safePart(session) {
		err = fmt.Errorf("invalid ref %q", ref)
	}
	return
}

func parseEvidenceRef(ref string) (source, sessionPrefix string, line int, ok bool) {
	source, tail, found := strings.Cut(ref, ":")
	if !found {
		return "", "", 0, false
	}
	sessionPrefix, lineText, found := strings.Cut(tail, "#L")
	if !found || source == "" || sessionPrefix == "" {
		return "", "", 0, false
	}
	line, err := strconv.Atoi(lineText)
	return source, sessionPrefix, line, err == nil && line > 0
}

func safePart(value string) bool {
	return value != "" && !strings.ContainsAny(value, `/\\`) && !strings.Contains(value, "..")
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
