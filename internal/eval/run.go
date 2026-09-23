package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"spindle/internal/llm"
	"spindle/internal/sleep"
)

const readerTools = "Read,Grep,Glob"

// readerDeny keeps the reader inside its materialized memory directory. Eval
// dirs live under /private/tmp, so denying /Users blocks the live corpus,
// Codex and Claude homes, and the repo.
var readerDeny = []string{"Read(//Users/**)", "Grep(//Users/**)", "Glob(//Users/**)", "Read(//private/tmp/spx/**)", "Grep(//private/tmp/spx/**)", "Glob(//private/tmp/spx/**)"}

func readerSystem(a Arm) string {
	if !a.Transcripts && a.Notes == "" {
		return `You answer questions about the user's past work with AI agents. You have no memory of past
sessions and no tools. Answer only if you actually know; otherwise say you don't know.
End with a line "CITATIONS:" (empty).`
	}
	var b strings.Builder
	b.WriteString(`You answer questions about the user's past work with AI agents, using read-only memory files
in the current directory. Search with Grep and Glob, then Read what matters.
`)
	if a.Transcripts {
		b.WriteString(`- transcripts/<source>/<session>/<NNNN>.md: past agent sessions. Frontmatter has cwd and time range.
  Each line starts with [item-id timestamp role] where role is user, assistant, or tool.
`)
	}
	if a.Notes != "" {
		b.WriteString(`- decisions/<project>/<id>.md: decision notes. Frontmatter has id, time, project, and evidence
  (transcript item IDs). The body says what was decided, who proposed it, and whether the user agreed.
`)
	}
	b.WriteString(`
Answer concisely. Give the latest decision if it changed, and say who decided it when you can tell:
the user or the agent, and whether the user agreed. If memory doesn't contain the answer, say you don't know.
End with a line "CITATIONS:" followed by the item IDs and note IDs you relied on, comma-separated.`)
	return b.String()
}

const judgeSystem = `You grade an answer to a question about a user's past work against gold statements.
For each gold statement: "supported" if the answer states it or clearly implies it; "contradicted" if
the answer states something incompatible with it; otherwise "missing".
abstained: the answer says it doesn't know or can't find it.
contradiction: the answer asserts anything that conflicts with the gold statements or gold attribution.
attribution: only when a gold attribution is given and the question asks who decided or whether the
user agreed. "correct" if the answer's account of who proposed it and the user's stance matches the
gold attribution, "wrong" if it conflicts, "missing" if not addressed. Otherwise "na".
Judge meaning, not wording. Ignore the CITATIONS line.`

const judgeSchema = `{"type":"object","properties":{"statements":{"type":"array","items":{"type":"string","enum":["supported","contradicted","missing"]}},"abstained":{"type":"boolean"},"contradiction":{"type":"boolean"},"attribution":{"type":"string","enum":["correct","wrong","missing","na"]},"reason":{"type":"string"}},"required":["statements","abstained","contradiction","attribution","reason"]}`

// Judgment is the judge's verdict on one answer.
type Judgment struct {
	Statements    []string `json:"statements"`
	Abstained     bool     `json:"abstained"`
	Contradiction bool     `json:"contradiction"`
	Attribution   string   `json:"attribution"`
	Reason        string   `json:"reason"`
}

// Result is one item's outcome in a run.
type Result struct {
	ItemID         string         `json:"item"`
	Kind           string         `json:"kind"`
	Arm            string         `json:"arm"`
	Answer         string         `json:"answer"`
	Citations      []string       `json:"citations"`
	CitationsValid int            `json:"citations_valid"`
	ToolCalls      []llm.ToolCall `json:"tool_calls"`
	Judgment       Judgment       `json:"judgment"`
	Correct        float64        `json:"correct"` // share of gold statements supported
	ReaderCost     float64        `json:"reader_cost_usd"`
	JudgeCost      float64        `json:"judge_cost_usd"`
	Turns          int            `json:"turns"`
	DurationMS     int64          `json:"duration_ms"`
	Error          string         `json:"error,omitempty"`
}

// RunOptions configures an eval run.
type RunOptions struct {
	CorpusRoot string
	Arm        Arm
	Items      []Item
	RunDir     string // results go here
	ScratchDir string // materialized memory, under /private/tmp
	Workers    int
	MaxTurns   int
}

// Run answers and judges every item for one arm.
func Run(ctx context.Context, o RunOptions) ([]Result, error) {
	idx, err := loadChunkIndex(o.CorpusRoot)
	if err != nil {
		return nil, err
	}
	var notes []sleep.Note
	if o.Arm.Notes != "" {
		if notes, err = sleep.ReadManifest(o.Arm.Notes); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(o.RunDir, 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu      sync.Mutex
		results []Result
		limited bool
		wg      sync.WaitGroup
		sem     = make(chan struct{}, o.Workers)
	)
	for _, it := range o.Items {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it Item) {
			defer wg.Done()
			defer func() { <-sem }()
			r, err := runItem(ctx, o, idx, notes, it)
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, llm.ErrUsageLimit) {
				limited = true
				cancel()
				return
			}
			if err != nil {
				r.Error = err.Error()
			}
			results = append(results, r)
		}(it)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].ItemID < results[j].ItemID })
	if err := writeJSONL(filepath.Join(o.RunDir, "results.jsonl"), results); err != nil {
		return results, err
	}
	if limited {
		return results, fmt.Errorf("stopped early: %w", llm.ErrUsageLimit)
	}
	return results, nil
}

var citationRe = regexp.MustCompile(citationPattern)

func runItem(ctx context.Context, o RunOptions, idx *chunkIndex, notes []sleep.Note, it Item) (Result, error) {
	r := Result{ItemID: it.ID, Kind: it.Kind, Arm: o.Arm.Name}
	dir := filepath.Join(o.ScratchDir, it.ID)
	defer os.RemoveAll(dir)
	if err := Materialize(dir, o.Arm, idx, notes, it.AskedAt); err != nil {
		return r, err
	}
	req := llm.Request{
		Model:    llm.Reader,
		System:   readerSystem(o.Arm),
		Prompt:   fmt.Sprintf("Today is %s.\nQuestion: %s", it.AskedAt.Format("2006-01-02 15:04 MST"), it.Question),
		Dir:      dir,
		MaxTurns: o.MaxTurns,
		Timeout:  8 * time.Minute,
	}
	if o.Arm.Transcripts || o.Arm.Notes != "" {
		req.Tools, req.Deny = strings.Split(readerTools, ","), readerDeny
	}
	res, err := llm.Run(ctx, req)
	r.Answer, r.ToolCalls, r.ReaderCost, r.Turns, r.DurationMS = res.Text, res.ToolCalls, res.CostUSD, res.Turns, res.DurationMS
	if err != nil {
		return r, err
	}
	if i := strings.LastIndex(res.Text, "CITATIONS:"); i >= 0 {
		r.Citations = uniq(citationRe.FindAllString(res.Text[i:], -1))
	}
	if r.CitationsValid, err = citationsValid(dir, r.Citations); err != nil {
		return r, err
	}

	var gold strings.Builder
	for i, g := range it.Gold {
		fmt.Fprintf(&gold, "%d. %s\n", i+1, g)
	}
	prompt := fmt.Sprintf("<question>%s</question>\n<gold_statements>\n%s</gold_statements>\n<gold_attribution>%s</gold_attribution>\n<question_kind>%s</question_kind>\n<answer>\n%s\n</answer>",
		it.Question, gold.String(), it.Attribution, it.Kind, res.Text)
	jres, err := llm.JSON(ctx, llm.Judge, judgeSystem, prompt, judgeSchema, &r.Judgment)
	r.JudgeCost = jres.CostUSD
	if err != nil {
		return r, err
	}
	supported := 0
	for _, s := range r.Judgment.Statements {
		if s == "supported" {
			supported++
		}
	}
	r.Correct = float64(supported) / float64(len(it.Gold))
	return r, nil
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// Summary aggregates a run, overall and by kind.
type Summary struct {
	Arm       string                        `json:"arm"`
	N         int                           `json:"n"`
	Errors    int                           `json:"errors"`
	Metrics   map[string]float64            `json:"metrics"`
	ByKind    map[string]map[string]float64 `json:"by_kind"`
	CostUSD   float64                       `json:"cost_usd"`
	Generated time.Time                     `json:"generated"`
}

// Summarize computes run metrics.
func Summarize(arm string, rs []Result) Summary {
	s := Summary{Arm: arm, Metrics: metrics(rs), ByKind: map[string]map[string]float64{}, Generated: time.Now().UTC()}
	byKind := map[string][]Result{}
	for _, r := range rs {
		s.CostUSD += r.ReaderCost + r.JudgeCost
		if r.Error != "" {
			s.Errors++
			continue
		}
		s.N++
		byKind[r.Kind] = append(byKind[r.Kind], r)
	}
	for k, v := range byKind {
		s.ByKind[k] = metrics(v)
	}
	return s
}

func metrics(rs []Result) map[string]float64 {
	var n, correct, contra, abst, reached, citeValid, cites, turns, cost, attrN, attrOK float64
	for _, r := range rs {
		if r.Error != "" {
			continue
		}
		n++
		correct += r.Correct
		if r.Judgment.Contradiction {
			contra++
		}
		if r.Judgment.Abstained {
			abst++
		}
		if len(r.ToolCalls) > 0 {
			reached++
		}
		cites += float64(len(r.Citations))
		citeValid += float64(r.CitationsValid)
		turns += float64(r.Turns)
		cost += r.ReaderCost
		if a := r.Judgment.Attribution; a != "na" && a != "" {
			attrN++
			if a == "correct" {
				attrOK++
			}
		}
	}
	if n == 0 {
		return map[string]float64{"n": 0}
	}
	m := map[string]float64{
		"n": n, "correct": correct / n, "contradiction": contra / n, "abstained": abst / n,
		"reached": reached / n, "turns": turns / n, "reader_cost_usd": cost / n,
	}
	if cites > 0 {
		m["citations_valid"] = citeValid / cites
	}
	if attrN > 0 {
		m["attribution"] = attrOK / attrN
		m["attribution_n"] = attrN
	}
	return m
}

// WriteSummary stores summary.json in the run directory.
func WriteSummary(runDir string, s Summary) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(filepath.Join(runDir, "summary.json"), b, 0o644)
}
