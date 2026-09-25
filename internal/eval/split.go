package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"spindle/internal/llm"
)

// Compound checklist items ("X was committed as abc123 and the tree was clean")
// grade as partial when a brief gets one half, which hides what was actually
// missed. Splitting them into single facts makes each grade mean one thing.
const checklistSplitSystem = `You split a grading checklist into single claims. Some input items bundle several distinct
claims about different things (e.g. "X was committed as abc123; ticket Y was already done; tests remain").
Return, for every item, the distinct claims it contains, so each can be graded covered or missing on its own.
- Split only where the parts are independent: a different subject, state, or decision. Keep a list or
  enumeration that belongs to one claim together as one claim ("coverage for connect, reconnect, start
  and stop" stays one). Most items yield 1 to 3 claims.
- Do not add, infer, generalize, or drop information. Keep exact identifiers (hashes, ticket IDs, numbers,
  names) in the claim they belong to.
- If an item is already a single claim, return it unchanged as a one-element list.
- Context a claim needs to be understood (e.g. which ticket or system) may be repeated in each claim.`

const checklistSplitSchema = `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"n":{"type":"integer"},"facts":{"type":"array","items":{"type":"string"}}},"required":["n","facts"]}}},"required":["items"]}`

// SplitChecklists returns copies of trials whose checklists hold one fact per
// item. ChecklistFrom records each fact's 1-based index in the original list.
func SplitChecklists(ctx context.Context, trials []Trial, workers int) ([]Trial, error) {
	out := make([]Trial, len(trials))
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []string
		sem  = make(chan struct{}, max(1, workers))
	)
	for i, t := range trials {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t Trial) {
			defer wg.Done()
			defer func() { <-sem }()
			split, err := splitChecklist(ctx, t)
			if err != nil && !errors.Is(err, llm.ErrUsageLimit) {
				split, err = splitChecklist(ctx, t) // occasional dropped item; one retry
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", t.ID, err))
				return
			}
			out[i] = split
		}(i, t)
	}
	wg.Wait()
	if len(errs) > 0 {
		return nil, fmt.Errorf("split checklists: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func splitChecklist(ctx context.Context, t Trial) (Trial, error) {
	var b strings.Builder
	for i, c := range t.Checklist {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c)
	}
	var res struct {
		Items []struct {
			N     int      `json:"n"`
			Facts []string `json:"facts"`
		} `json:"items"`
	}
	if _, err := llm.JSONRequest(ctx, llm.Request{Model: llm.Labeler, Effort: "medium", System: checklistSplitSystem, Prompt: "<checklist>\n" + b.String() + "</checklist>", Schema: checklistSplitSchema}, &res); err != nil {
		return t, err
	}
	facts := make([][]string, len(t.Checklist))
	for _, it := range res.Items {
		if it.N >= 1 && it.N <= len(t.Checklist) {
			facts[it.N-1] = append(facts[it.N-1], it.Facts...)
		}
	}
	orig := t.Checklist
	t.Checklist, t.ChecklistFrom = nil, nil
	for i, fs := range facts {
		if len(fs) == 0 {
			// The model sometimes drops an item; keeping it unsplit loses nothing.
			fs = []string{orig[i]}
		}
		for _, f := range fs {
			t.Checklist = append(t.Checklist, strings.TrimSpace(f))
			t.ChecklistFrom = append(t.ChecklistFrom, i+1)
		}
	}
	return t, nil
}
