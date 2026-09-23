// Package sleep turns corpus chunks into memory notes. v0 extracts decisions only.
package sleep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/llm"
)

// PromptVersion changes whenever the extractor prompt or note format changes,
// which invalidates the per-chunk cache.
const PromptVersion = "decisions-v1"

const contextItems = 8 // trailing items of the previous chunk, so decisions spanning a boundary keep their proposal

const extractSystem = `You extract DECISIONS from a transcript chunk of a user working with an AI agent.

A decision is a choice about how something should be: a design, a rule, an approach, a scope, or a name.
Include rejections: things the user turned down. Include changes to earlier decisions.
Not decisions: tasks performed, facts looked up, questions, brainstorm options nobody chose, and the
agent's own plans for how to do the current task.

For each decision write a short note (1-4 sentences, plain prose):
1. The subject and the choice, specific enough to answer "what did we decide about <subject>?"
   Include exact values (numbers, bands, names) when they are part of the choice.
2. Attribution: who proposed it (the user or the agent) and the user's stance: stated it themselves,
   explicitly endorsed it, went along with it without comment, never addressed it, rejected it, or
   corrected it. Moving on is not agreeing: if the agent proposed something and the user never engaged,
   say so. Quote the user's key words briefly when they decide or reject.
3. Why, only if a reason was stated.
4. What it replaces, if it changes an earlier decision.

Do not record where things live in files or code, or copy long rule text; point at the subject.
Name the project or topic so the note stands alone (e.g. "Eldspire:").
Evidence: cite the item IDs (e.g. codex:01a07de8#L315) where the decision was proposed and where the
user reacted. Only cite IDs that appear in the input. Items marked context come from the previous chunk.
Return an empty list if the chunk contains no decisions.`

const extractSchema = `{"type":"object","properties":{"decisions":{"type":"array","items":{"type":"object","properties":{"note":{"type":"string"},"evidence":{"type":"array","items":{"type":"string"}}},"required":["note","evidence"]}}},"required":["decisions"]}`

// Decision is one extracted note before it's written to disk.
type Decision struct {
	Note     string   `json:"note"`
	Evidence []string `json:"evidence"`
}

// ChunkResult is the cached extraction for one chunk.
type ChunkResult struct {
	Chunk       string     `json:"chunk"`
	ContentHash string     `json:"content_hash"`
	Prompt      string     `json:"prompt"`
	AvailableAt time.Time  `json:"available_at"` // chunk end: nothing here may be used before this time
	Project     string     `json:"project"`
	Decisions   []Decision `json:"decisions"`
	Times       []string   `json:"times"` // per decision: time of its latest evidence item
	CostUSD     float64    `json:"cost_usd"`
}

// Options configures a sleep run.
type Options struct {
	CorpusRoot string
	OutRoot    string // e.g. data/memory/decisions-v1
	Slice      string
	Workers    int
	Model      string
}

// Run extracts decisions from every chunk in the slice, reusing cached results.
func Run(ctx context.Context, o Options) (extracted, cached int, cost float64, err error) {
	paths, err := corpus.ChunkPaths(o.CorpusRoot)
	if err != nil {
		return 0, 0, 0, err
	}
	sort.Strings(paths)
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, o.Workers)
	)
	for i, p := range paths {
		h, err := corpus.ReadChunkHeader(p)
		if err != nil {
			return 0, 0, 0, err
		}
		if !strings.Contains(strings.ToLower(h.Cwd), strings.ToLower(o.Slice)) {
			continue
		}
		var prev string
		if h.Index > 1 && i > 0 {
			prev = filepath.Join(filepath.Dir(p), fmt.Sprintf("%04d.md", h.Index-1))
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p, prev string) {
			defer wg.Done()
			defer func() { <-sem }()
			hit, c, err := extractChunk(ctx, o, p, prev)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p, err))
				return
			}
			if hit {
				cached++
			} else {
				extracted++
				cost += c
			}
		}(p, prev)
	}
	wg.Wait()
	if len(errs) > 0 {
		return extracted, cached, cost, fmt.Errorf("%d chunks failed; first: %w", len(errs), errs[0])
	}
	return extracted, cached, cost, WriteNotes(o.OutRoot)
}

func cachePath(outRoot, chunkPath string) string {
	parts := strings.Split(filepath.ToSlash(chunkPath), "/")
	n := len(parts)
	return filepath.Join(outRoot, "cache", parts[n-3], parts[n-2], strings.TrimSuffix(parts[n-1], ".md")+".json")
}

func extractChunk(ctx context.Context, o Options, path, prevPath string) (bool, float64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, 0, err
	}
	sum := sha256.Sum256(append(raw, []byte(PromptVersion)...))
	hash := hex.EncodeToString(sum[:])
	cp := cachePath(o.OutRoot, path)
	var old ChunkResult
	if b, err := os.ReadFile(cp); err == nil && json.Unmarshal(b, &old) == nil && old.ContentHash == hash {
		return true, 0, nil
	}

	c, err := corpus.ReadChunk(path)
	if err != nil {
		return false, 0, err
	}
	var ctxItems []corpus.Item
	if prevPath != "" {
		if pc, err := corpus.ReadChunk(prevPath); err == nil {
			ctxItems = lastMessages(pc.Items, contextItems)
		}
	}
	known := map[string]time.Time{}
	var b strings.Builder
	for _, it := range ctxItems {
		known[it.ID] = it.Time
		fmt.Fprintf(&b, "[%s %s context] %s\n", it.ID, it.Role, it.Text)
	}
	for _, it := range c.Items {
		if it.Role == corpus.RoleTool {
			continue
		}
		known[it.ID] = it.Time
		fmt.Fprintf(&b, "[%s %s] %s\n", it.ID, it.Role, it.Text)
	}

	var out struct {
		Decisions []Decision `json:"decisions"`
	}
	res, err := llm.JSON(ctx, o.Model, extractSystem, b.String(), extractSchema, &out)
	if err != nil {
		return false, res.CostUSD, err
	}
	result := ChunkResult{Chunk: path, ContentHash: hash, Prompt: PromptVersion, AvailableAt: c.End, Project: Project(c.Cwd), CostUSD: res.CostUSD}
	for _, d := range out.Decisions {
		var ev []string
		var latest time.Time
		for _, id := range d.Evidence {
			if t, ok := known[id]; ok {
				ev = append(ev, id)
				if t.After(latest) {
					latest = t
				}
			}
		}
		if len(ev) == 0 || strings.TrimSpace(d.Note) == "" {
			continue // uncited notes can't be checked, so they aren't memory
		}
		result.Decisions = append(result.Decisions, Decision{Note: strings.TrimSpace(d.Note), Evidence: ev})
		result.Times = append(result.Times, latest.UTC().Format(time.RFC3339))
	}
	if err := os.MkdirAll(filepath.Dir(cp), 0o755); err != nil {
		return false, res.CostUSD, err
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	return false, res.CostUSD, os.WriteFile(cp, data, 0o644)
}

func lastMessages(items []corpus.Item, n int) []corpus.Item {
	var msgs []corpus.Item
	for _, it := range items {
		if it.Role != corpus.RoleTool {
			msgs = append(msgs, it)
		}
	}
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	return msgs
}

// Project names a slice of work from a session's cwd.
func Project(cwd string) string {
	return strings.ToLower(filepath.Base(cwd))
}
