package eval

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"spindle/internal/corpus"
	"spindle/internal/llm"
)

// The trial labeler saw only a few items before the pickup and many after it,
// so it often described a later moment of the same long session. Verification
// re-grounds every claim in transcript items from before the cutoff: a claim
// survives only with a citation that code confirms is pre-cutoff.
const checklistVerifySystem = `You audit the answer key of a continuation trial. At the cutoff a user picked up earlier
work; a fresh agent will start at that moment with only the transcript BEFORE the cutoff. You see that
pre-cutoff transcript (most recent part), a short window after the cutoff, and the candidate claims.

For each claim:
- verdict "supported" only if the pre-cutoff transcript establishes it as true at the cutoff. Cite the item
  IDs (exactly as shown, e.g. "codex:019e85fe#L30310") that establish it.
- verdict "unsupported" if it depends on anything at or after the cutoff, even if it became true minutes
  later, or if the pre-cutoff transcript doesn't show it.
- verdict "task" if it only restates the user's request or the trial task.
- importance: "must" if a continuing agent that lacked it would go wrong; "nice" if true but not needed.
You may add up to 3 missing claims a continuing agent must know, each with pre-cutoff citations.
The after-cutoff window is only a hint about what mattered next. Never use it as evidence.`

const checklistVerifySchema = `{"type":"object","properties":{"claims":{"type":"array","items":{"type":"object","properties":{"n":{"type":"integer"},"verdict":{"type":"string","enum":["supported","unsupported","task"]},"importance":{"type":"string","enum":["must","nice"]},"cites":{"type":"array","items":{"type":"string"}},"reason":{"type":"string"}},"required":["n","verdict","importance","cites","reason"]}},"added":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"},"cites":{"type":"array","items":{"type":"string"}}},"required":["text","cites"]}}},"required":["claims","added"]}`

const (
	verifyBeforeBudget = 90000 // characters of pre-cutoff transcript shown
	verifyAfterItems   = 12
	verifyAfterSpan    = 45 * time.Minute
)

// VerifyReport says what verification did to one trial.
type VerifyReport struct {
	Trial       string   `json:"trial"`
	Kept        int      `json:"kept"`
	Unsupported int      `json:"unsupported"`
	Task        int      `json:"task"`
	BadCites    int      `json:"bad_cites"` // claimed supported, but no citation resolved before the cutoff
	Added       int      `json:"added"`
	Dropped     bool     `json:"dropped"` // fewer than the minimum claims survived
	Reasons     []string `json:"reasons,omitempty"`
}

// VerifyTrials returns the trials whose checklists survive verification, plus a
// report per input trial. Trials left with fewer than minClaims claims are
// dropped. Kept claims carry ChecklistImportance and ChecklistCites.
func VerifyTrials(ctx context.Context, corpusRoot string, trials []Trial, workers, minClaims int) ([]Trial, []VerifyReport, error) {
	out := make([]*Trial, len(trials))
	reps := make([]VerifyReport, len(trials))
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
			vt, rep, err := verifyTrial(ctx, corpusRoot, t, minClaims)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", t.ID, err))
				return
			}
			reps[i] = rep
			if !rep.Dropped {
				out[i] = &vt
			}
		}(i, t)
	}
	wg.Wait()
	if len(errs) > 0 {
		return nil, nil, fmt.Errorf("verify trials: %s", strings.Join(errs, "; "))
	}
	var kept []Trial
	for _, t := range out {
		if t != nil {
			kept = append(kept, *t)
		}
	}
	return kept, reps, nil
}

func verifyTrial(ctx context.Context, corpusRoot string, t Trial, minClaims int) (Trial, VerifyReport, error) {
	rep := VerifyReport{Trial: t.ID}
	handles := map[string]bool{t.Source + ":" + t.Session: true}
	for _, h := range t.Prior {
		handles[h] = true
	}
	var before, after []corpus.Item
	for h := range handles {
		source, session, _ := strings.Cut(h, ":")
		chunks, err := corpus.ReadSession(corpusRoot, source, session)
		if err != nil {
			return t, rep, err
		}
		for _, ch := range chunks {
			for _, it := range ch.Items {
				switch {
				case it.Time.Before(t.AskedAt):
					before = append(before, it)
				case h == t.Source+":"+t.Session && it.Role != corpus.RoleTool && it.Time.Sub(t.AskedAt) <= verifyAfterSpan:
					after = append(after, it)
				}
			}
		}
	}
	sort.Slice(before, func(i, j int) bool { return before[i].Time.Before(before[j].Time) })
	sort.Slice(after, func(i, j int) bool { return after[i].Time.Before(after[j].Time) })
	if len(after) > verifyAfterItems {
		after = after[:verifyAfterItems]
	}

	// Most recent pre-cutoff items that fit the budget; tool traffic is already clipped in the corpus.
	var lines []string
	used := 0
	for i := len(before) - 1; i >= 0 && used < verifyBeforeBudget; i-- {
		it := before[i]
		l := fmt.Sprintf("[%s %s %s] %s", it.ID, it.Time.Format("2006-01-02 15:04"), it.Role, clip(it.Text, 1500))
		used += len(l)
		lines = append(lines, l)
	}
	var b strings.Builder
	b.WriteString("<before_cutoff>\n")
	for i := len(lines) - 1; i >= 0; i-- {
		b.WriteString(lines[i] + "\n")
	}
	fmt.Fprintf(&b, "</before_cutoff>\n<cutoff time=%q>\n%s\n</cutoff>\n<trial_task>%s</trial_task>\n<after_cutoff_hint>\n", t.AskedAt.Format("2006-01-02 15:04"), t.Message, t.Task)
	for _, it := range after {
		fmt.Fprintf(&b, "[%s %s] %s\n", it.Time.Format("2006-01-02 15:04"), it.Role, clip(it.Text, 1500))
	}
	b.WriteString("</after_cutoff_hint>\n<claims>\n")
	for i, c := range t.Checklist {
		fmt.Fprintf(&b, "%d. %s\n", i+1, c)
	}
	b.WriteString("</claims>")

	var res struct {
		Claims []struct {
			N          int      `json:"n"`
			Verdict    string   `json:"verdict"`
			Importance string   `json:"importance"`
			Cites      []string `json:"cites"`
			Reason     string   `json:"reason"`
		} `json:"claims"`
		Added []struct {
			Text  string   `json:"text"`
			Cites []string `json:"cites"`
		} `json:"added"`
	}
	req := llm.Request{Model: llm.Labeler, Effort: "medium", System: checklistVerifySystem, Prompt: b.String(), Schema: checklistVerifySchema}
	if _, err := llm.JSONRequest(ctx, req, &res); err != nil {
		if errors.Is(err, llm.ErrUsageLimit) {
			return t, rep, err
		}
		if _, err = llm.JSONRequest(ctx, req, &res); err != nil {
			return t, rep, err
		}
	}

	resolve := citeResolver(before)
	v := t
	v.Checklist, v.ChecklistFrom, v.ChecklistImportance, v.ChecklistCites = nil, nil, nil, nil
	seen := map[int]bool{}
	for _, c := range res.Claims {
		if c.N < 1 || c.N > len(t.Checklist) || seen[c.N] {
			continue
		}
		seen[c.N] = true
		switch c.Verdict {
		case "task":
			rep.Task++
			continue
		case "unsupported":
			rep.Unsupported++
			rep.Reasons = append(rep.Reasons, fmt.Sprintf("%d: %s", c.N, c.Reason))
			continue
		}
		cites := resolve(c.Cites)
		if len(cites) == 0 {
			rep.BadCites++
			rep.Reasons = append(rep.Reasons, fmt.Sprintf("%d: no pre-cutoff citation resolved (%v)", c.N, c.Cites))
			continue
		}
		from := c.N
		if len(t.ChecklistFrom) == len(t.Checklist) {
			from = t.ChecklistFrom[c.N-1]
		}
		v.Checklist = append(v.Checklist, t.Checklist[c.N-1])
		v.ChecklistFrom = append(v.ChecklistFrom, from)
		v.ChecklistImportance = append(v.ChecklistImportance, c.Importance)
		v.ChecklistCites = append(v.ChecklistCites, cites)
		rep.Kept++
	}
	for _, a := range res.Added {
		cites := resolve(a.Cites)
		if len(cites) == 0 || strings.TrimSpace(a.Text) == "" {
			continue
		}
		v.Checklist = append(v.Checklist, strings.TrimSpace(a.Text))
		v.ChecklistFrom = append(v.ChecklistFrom, 0) // 0: added by verification
		v.ChecklistImportance = append(v.ChecklistImportance, "must")
		v.ChecklistCites = append(v.ChecklistCites, cites)
		rep.Added++
	}
	rep.Dropped = len(v.Checklist) < minClaims
	return v, rep, nil
}

var citeLine = regexp.MustCompile(`#?L(\d+)$`)

// citeResolver maps model citations to pre-cutoff item IDs. Models shorten IDs
// ("L2934", "019e85fe#L2934"), so a citation falls back to its line number when
// that line is unique among the pre-cutoff items.
func citeResolver(before []corpus.Item) func([]string) []string {
	byID := map[string]bool{}
	byLine := map[string][]string{}
	for _, it := range before {
		byID[it.ID] = true
		if m := citeLine.FindStringSubmatch(it.ID); m != nil {
			byLine[m[1]] = append(byLine[m[1]], it.ID)
		}
	}
	return func(cites []string) []string {
		var out []string
		for _, c := range cites {
			c = strings.Trim(strings.TrimSpace(c), "[]")
			if byID[c] {
				out = append(out, c)
				continue
			}
			if m := citeLine.FindStringSubmatch(c); m != nil && len(byLine[m[1]]) == 1 {
				out = append(out, byLine[m[1]][0])
			}
		}
		return out
	}
}
