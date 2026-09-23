package eval

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"spindle/internal/corpus"
	"spindle/internal/llm"
)

// recallCue is a cheap prefilter for messages that reach back to earlier
// work. It favors recall over precision; the classifier does the filtering.
var recallCue = regexp.MustCompile(`(?i)\b(decid|decision|did we|have we|we said|we agreed|agree|did i|i said|i thought we|why (is|are|do|did|does)|when did|what did we|what was|what's the|remind|again|remember|final|chose|chosen|settled|landed on|originally|before we|we had|we changed|change[sd]? (from|to)|still|rejected|dismissed|dropped)\b`)

// MineCandidates prefilters user messages from sessions in the slice.
func MineCandidates(corpusRoot, slice string) ([]Candidate, error) {
	paths, err := corpus.ChunkPaths(corpusRoot)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, p := range paths {
		c, err := corpus.ReadChunk(p)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(strings.ToLower(c.Cwd), strings.ToLower(slice)) {
			continue
		}
		for _, it := range c.Items {
			if it.Role != corpus.RoleUser || len(it.Text) > 1500 || !recallCue.MatchString(it.Text) {
				continue
			}
			out = append(out, Candidate{ItemID: it.ID, Source: c.Source, Session: c.Session, Cwd: c.Cwd, AskedAt: it.Time, Text: it.Text})
		}
	}
	return out, nil
}

const classifySystem = `You classify messages a user sent to a coding/design agent.
Decide whether each message asks the agent to RECALL A PAST DECISION made in earlier conversation:
what was decided (current), who decided or whether the user agreed (who), why it was decided (why),
whether/when it changed (changed), or what was rejected (rejected).
Not recall: new requests, new proposals, instructions, feedback on the current answer, questions about
general knowledge or external games/tools, and "do X again" requests.`

const classifySchema = `{"type":"object","properties":{"results":{"type":"array","items":{"type":"object","properties":{"n":{"type":"integer"},"recall":{"type":"boolean"},"kind":{"type":"string","enum":["current","who","why","changed","rejected","none"]},"reason":{"type":"string"}},"required":["n","recall","kind","reason"]}}},"required":["results"]}`

// ClassifyCandidates keeps candidates that ask to recall a past decision.
func ClassifyCandidates(ctx context.Context, cands []Candidate, model string, workers int) ([]Candidate, float64, error) {
	const batch = 25
	type job struct{ start, end int }
	var jobs []job
	for i := 0; i < len(cands); i += batch {
		jobs = append(jobs, job{i, min(i+batch, len(cands))})
	}
	var (
		mu   sync.Mutex
		kept []Candidate
		cost float64
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, workers)
	)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			var b strings.Builder
			for i := j.start; i < j.end; i++ {
				fmt.Fprintf(&b, "<message n=%d>\n%s\n</message>\n", i-j.start, strings.TrimSpace(cands[i].Text))
			}
			var out struct {
				Results []struct {
					N      int    `json:"n"`
					Recall bool   `json:"recall"`
					Kind   string `json:"kind"`
					Reason string `json:"reason"`
				} `json:"results"`
			}
			res, err := llm.JSON(ctx, model, classifySystem, b.String(), classifySchema, &out)
			mu.Lock()
			defer mu.Unlock()
			cost += res.CostUSD
			if err != nil {
				errs = append(errs, err)
				return
			}
			for _, r := range out.Results {
				idx := j.start + r.N
				if r.Recall && r.Kind != "none" && idx >= j.start && idx < j.end {
					c := cands[idx]
					c.Kind, c.Classify = r.Kind, r.Reason
					kept = append(kept, c)
				}
			}
		}(j)
	}
	wg.Wait()
	if len(errs) > 0 {
		return kept, cost, fmt.Errorf("%d classify batches failed; first: %w", len(errs), errs[0])
	}
	return kept, cost, nil
}
