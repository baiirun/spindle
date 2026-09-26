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

// Replaying a pickup as a brand-new session needs an opening request that
// makes sense without the thread: many real pickups are mid-conversation
// ("isn't that what the other agent recommended?", "keep going"). The opener
// names the effort the way the user would, and must not hand the agent the
// answers the checklist grades.
const openerSystem = `You turn a real "pick the work back up" moment into the opening message of a BRAND-NEW agent
session. The new agent has no thread and no memory of earlier sessions; it can only search past transcripts.
You see the user's real message, the project directory, the earlier session's opening request, and the
checklist the agent will be graded on.
- If the real message already works as a vague first message of a new session (it names the topic, has no
  identifiers, and asks something self-explanatory), keep it verbatim: verdict "verbatim".
- Otherwise write a short, vague opener in the user's voice (one sentence, lowercase is fine) that names only
  the project or topic and the kind of ask, the way people restart work without a link: verdict "rewritten".
  E.g. "where were we on the eldspire rules?", "let's go back to the aether query engine", "can you take over
  the spindle work?".
- No identifiers at all: no session, ticket or commit IDs, file paths, or skill links.
- Never include facts from the checklist that aren't in the real message: no decisions, results, file names,
  commit hashes, ticket IDs, counts or next steps. Naming the project or effort is fine.`

const openerSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["verbatim","rewritten"]},"opener":{"type":"string"},"reason":{"type":"string"}},"required":["verdict","opener","reason"]}`

// OpenerReview records how one trial's opening request was produced.
type OpenerReview struct {
	Trial    string   `json:"trial"`
	Original string   `json:"original"`
	Opener   string   `json:"opener"`
	Verdict  string   `json:"verdict"`
	Reason   string   `json:"reason"`
	Leaks    []string `json:"leaks,omitempty"` // checklist identifiers the opener adds; reviewed by hand
}

var openerIdent = regexp.MustCompile("`([^`]{3,60})`|\\b([0-9a-f]{7,40})\\b|\\b((?:ts|ep)-[0-9a-f]{6})\\b")

// AsNewSessions returns copies of trials replayed as brand-new sessions: mode
// wake (no prior handle given), with Task set to an opening request.
func AsNewSessions(ctx context.Context, corpusRoot string, trials []Trial, workers int) ([]Trial, []OpenerReview, error) {
	out := make([]Trial, len(trials))
	revs := make([]OpenerReview, len(trials))
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
			nt, rev, err := asNewSession(ctx, corpusRoot, t)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", t.ID, err))
				return
			}
			out[i], revs[i] = nt, rev
		}(i, t)
	}
	wg.Wait()
	if len(errs) > 0 {
		return nil, nil, fmt.Errorf("openers: %s", strings.Join(errs, "; "))
	}
	return out, revs, nil
}

func asNewSession(ctx context.Context, corpusRoot string, t Trial) (Trial, OpenerReview, error) {
	firstAsk := ""
	if len(t.Prior) > 0 {
		source, session, _ := strings.Cut(t.Prior[0], ":")
		if chunks, err := corpus.ReadSession(corpusRoot, source, session); err == nil {
		find:
			for _, ch := range chunks {
				for _, it := range ch.Items {
					if it.Role == corpus.RoleUser && len(strings.TrimSpace(it.Text)) >= wideMinChars {
						firstAsk = clip(oneLine(it.Text), 400)
						break find
					}
				}
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<project_dir>%s</project_dir>\n<earlier_session_opening>%s</earlier_session_opening>\n<real_message>\n%s\n</real_message>\n<checklist_do_not_leak>\n", t.Cwd, firstAsk, t.Message)
	for _, c := range t.Checklist {
		b.WriteString("- " + c + "\n")
	}
	b.WriteString("</checklist_do_not_leak>")
	var res struct {
		Verdict string `json:"verdict"`
		Opener  string `json:"opener"`
		Reason  string `json:"reason"`
	}
	if _, err := llm.JSONRequest(ctx, llm.Request{Model: llm.Labeler, Effort: "medium", System: openerSystem, Prompt: b.String(), Schema: openerSchema}, &res); err != nil {
		return t, OpenerReview{}, err
	}
	opener := strings.TrimSpace(res.Opener)
	if res.Verdict == "verbatim" || opener == "" {
		opener = t.Message
	}
	rev := OpenerReview{Trial: t.ID, Original: t.Message, Opener: opener, Verdict: res.Verdict, Reason: res.Reason}
	seen := map[string]bool{}
	for _, c := range t.Checklist {
		for _, m := range openerIdent.FindAllStringSubmatch(c, -1) {
			id := m[1] + m[2] + m[3]
			if !seen[id] && strings.Contains(opener, id) && !strings.Contains(t.Message, id) {
				seen[id] = true
				rev.Leaks = append(rev.Leaks, id)
			}
		}
	}
	nt := t
	nt.Mode, nt.Task = "wake", opener
	nt.Notes = strings.TrimSpace(nt.Notes + " [new-session replay; original task: " + clip(oneLine(t.Task), 300) + "]")
	return nt, rev, nil
}
