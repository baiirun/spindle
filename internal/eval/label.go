package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"spindle/internal/corpus"
	"spindle/internal/llm"
)

const (
	labelBefore     = 20   // items of context before the question
	labelAfter      = 30   // items after: the answer and the user's follow-ups
	labelItemBefore = 1500 // chars per item
	labelItemAfter  = 3000
)

const labelSystem = `You build a gold-standard eval item from a real conversation between a user and an AI agent.
The user asked a question that recalls a past decision. You see the conversation before the question,
the question, and what happened after: the agent's answer and the user's follow-ups.

Decide the TRUE answer as of the moment the question was asked, using all evidence in the window.
The agent's answer can be wrong. Later user corrections in the window override it. If the user says
they never agreed to something, record that.

Rules:
- keep=false if: it isn't really asking to recall a decision; the answer can't be determined from the
  window; or the "answer" is a NEW decision made after the question rather than something established before.
- question: rewrite as a standalone question a fresh agent with no context could understand. Name the
  project or topic (e.g. "In the Eldspire TTRPG design, ..."). Never include the answer or hint at it.
- gold: 1-4 short, atomic, checkable statements that a correct answer must contain. Prefer the facts
  the user needed. Include exact values (numbers, names, bands) when they matter.
- attribution: who proposed the decision (user, agent, other) and the user's stance (stated, endorsed,
  adopted without comment, never addressed, rejected, corrected), if the evidence shows it. Empty if unclear.
- kind: current | who | why | changed | rejected.
- confidence: high only if the evidence clearly establishes the gold. Otherwise medium or low.`

const labelSchema = `{"type":"object","properties":{"keep":{"type":"boolean"},"question":{"type":"string"},"kind":{"type":"string","enum":["current","who","why","changed","rejected"]},"gold":{"type":"array","items":{"type":"string"}},"attribution":{"type":"string"},"confidence":{"type":"string","enum":["high","medium","low"]},"notes":{"type":"string"}},"required":["keep","question","kind","gold","attribution","confidence","notes"]}`

type labelOutput struct {
	Keep        bool     `json:"keep"`
	Question    string   `json:"question"`
	Kind        string   `json:"kind"`
	Gold        []string `json:"gold"`
	Attribution string   `json:"attribution"`
	Confidence  string   `json:"confidence"`
	Notes       string   `json:"notes"`
}

// LabelCandidates drafts gold items. Each labeler verdict (kept or dropped) is
// cached under cacheDir, so an interrupted run resumes without repeating work.
// It stops scheduling new calls once the account hits its usage limit.
func LabelCandidates(ctx context.Context, corpusRoot, cacheDir string, cands []Candidate, workers int) ([]Item, float64, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu      sync.Mutex
		items   []Item
		cost    float64
		errs    []error
		limited bool
		wg      sync.WaitGroup
		sem     = make(chan struct{}, workers)
	)
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c Candidate) {
			defer wg.Done()
			defer func() { <-sem }()
			cp := filepath.Join(cacheDir, strings.NewReplacer(":", "_", "#", "_").Replace(c.ItemID)+".json")
			var out labelOutput
			if b, err := os.ReadFile(cp); err != nil || json.Unmarshal(b, &out) != nil {
				prompt, err := labelPrompt(corpusRoot, c)
				if err == nil {
					var res llm.Result
					res, err = llm.JSON(ctx, llm.Labeler, labelSystem, prompt, labelSchema, &out)
					mu.Lock()
					cost += res.CostUSD
					mu.Unlock()
				}
				if err != nil {
					mu.Lock()
					defer mu.Unlock()
					if errors.Is(err, llm.ErrUsageLimit) {
						limited = true
						cancel()
						return
					}
					if ctx.Err() == nil {
						errs = append(errs, fmt.Errorf("%s: %w", c.ItemID, err))
					}
					return
				}
				b, _ := json.Marshal(out)
				if err := os.WriteFile(cp, b, 0o644); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
			}
			if !out.Keep || len(out.Gold) == 0 {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			items = append(items, Item{
				Question: out.Question, Original: c.Text, ItemID: c.ItemID, Source: c.Source,
				Session: c.Session, Cwd: c.Cwd, AskedAt: c.AskedAt, Kind: out.Kind, Gold: out.Gold,
				Attribution: out.Attribution, Confidence: out.Confidence, Notes: out.Notes,
			})
		}(c)
	}
	wg.Wait()
	if limited {
		return items, cost, fmt.Errorf("stopped early: %w; rerun to resume", llm.ErrUsageLimit)
	}
	if len(errs) > 0 {
		return items, cost, fmt.Errorf("%d label calls failed; first: %w", len(errs), errs[0])
	}
	return items, cost, nil
}

func labelPrompt(corpusRoot string, c Candidate) (string, error) {
	chunks, err := corpus.ReadSession(corpusRoot, c.Source, c.Session)
	if err != nil {
		return "", err
	}
	var all []corpus.Item
	for _, ch := range chunks {
		for _, it := range ch.Items {
			if it.Role != corpus.RoleTool {
				all = append(all, it)
			}
		}
	}
	at := -1
	for i, it := range all {
		if it.ID == c.ItemID {
			at = i
			break
		}
	}
	if at < 0 {
		return "", fmt.Errorf("%s not found in session", c.ItemID)
	}
	var b strings.Builder
	b.WriteString("<before>\n")
	for _, it := range all[max(0, at-labelBefore):at] {
		fmt.Fprintf(&b, "[%s %s] %s\n", it.Time.Format("2006-01-02 15:04"), it.Role, clip(it.Text, labelItemBefore))
	}
	fmt.Fprintf(&b, "</before>\n<question asked_at=%q>\n%s\n</question>\n<after>\n", all[at].Time.Format("2006-01-02 15:04"), all[at].Text)
	for _, it := range all[at+1 : min(len(all), at+1+labelAfter)] {
		fmt.Fprintf(&b, "[%s %s] %s\n", it.Time.Format("2006-01-02 15:04"), it.Role, clip(it.Text, labelItemAfter))
	}
	b.WriteString("</after>\n")
	return b.String(), nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …"
}
