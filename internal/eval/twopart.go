package eval

import (
	"context"
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

// Two-part continuation summary (docs/variants.md, "Proposed design"). At each
// real compaction point the thread gets:
//   - Now: rewritten fresh from the preceding window. Fresh rewrites are good at
//     the current state; the latest ask and last exchange are copied by code.
//   - Ledger: carried forward from the previous compaction and changed only by
//     add / replace / drop edits that code applies (memory v3's mechanism,
//     narrowed to decisions, tried-or-rejected, and standing preferences).
//     Replacing an entry records what it replaced, so a change stays visible.
//
// Ledger steps are chained, so each is cached under twoPartCache and a rebuild
// resumes from the last cached step. Now parts are cached per compaction.

const twoPartCache = "data/eval/two-part-cache"

// Budgets enforced by code, not asked of the model alone.
const (
	ledgerBudget     = 16000 // rendered characters the model is told to stay under
	ledgerHardCap    = 20000 // above this, code evicts the least recently touched entries
	ledgerFieldChars = 400
	ledgerSpanChars  = 300000 // a span between compactions larger than this is folded in chunks
	nowBudget        = 6000
	verbatimChars    = 3000
)

var ledgerKinds = []struct{ Kind, Title string }{
	{"decision", "Decisions in force"},
	{"tried", "Tried or rejected"},
	{"preference", "Standing preferences and corrections"},
}

// LedgerEntry is one carried-forward line.
type LedgerEntry struct {
	Kind      string   `json:"kind"`
	Text      string   `json:"text"`
	Why       string   `json:"why"`
	Who       string   `json:"who"`
	UserWords string   `json:"user_words,omitempty"`
	Replaced  string   `json:"replaced,omitempty"` // set by code on replace
	Date      string   `json:"date"`
	Cites     []string `json:"cites"`
	Step      int      `json:"step"`
}

// LedgerChange logs every replace, drop and eviction, so stale entries can be audited.
type LedgerChange struct {
	Step   int    `json:"step"`
	Op     string `json:"op"`
	Key    string `json:"key"`
	Old    string `json:"old"`
	New    string `json:"new,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Ledger is the state edits apply to.
type Ledger struct {
	Entries  map[string]LedgerEntry `json:"entries"`
	Log      []LedgerChange         `json:"log"`
	AsOf     time.Time              `json:"as_of"`
	Step     int                    `json:"step"`
	Ops      int                    `json:"ops"`       // ops received at this step
	Skipped  int                    `json:"skipped"`   // ops code could not apply
	BadCites int                    `json:"bad_cites"` // cites not found in the conversation, dropped
}

type ledgerOp struct {
	Op        string   `json:"op"`
	Kind      string   `json:"kind"`
	Key       string   `json:"key"`
	Text      string   `json:"text"`
	Why       string   `json:"why"`
	Who       string   `json:"who"`
	UserWords string   `json:"user_words"`
	Date      string   `json:"date"`
	Reason    string   `json:"reason"`
	Cites     []string `json:"cites"`
}

const ledgerSystem = `You keep the ledger of one long-running conversation, as the assistant in it. The ledger is one half of the
continuation summary a future assistant gets when the context is compacted; the other half (the current state and
next step) is written separately, so leave current progress out of the ledger. You receive the current ledger and
the conversation since the last compaction. Return the edit operations that bring the ledger up to date, as JSON.
Code applies them.

Entry kinds:
- decision: a decision or rule currently in force. text: what was decided, stated fully enough to apply it (keep
  exact values, names, numbers, wording). why: the reason, in the user's reasoning where possible. who: "user"
  (the user stated it), "user approved" (the assistant proposed it and the user agreed; put the user's approving
  words in user_words, verbatim), or "assistant" (an implementation choice the assistant made and the user did not
  object to).
- tried: something tried or rejected, and why it failed or was turned down. who: who rejected it.
- preference: a standing preference or correction from the user about how to work. Put the user's words in
  user_words.

Operations (fill unused fields with "" or []):
- add: a new entry. key: a short stable topic name.
- replace: an existing entry changed. Reuse its exact key. Code keeps the old text as "replaced", so state the new
  version fully and give the reason in why.
- drop: an entry no longer holds or no longer matters. key, reason.

Rules:
- Check every existing entry against the new conversation. When the conversation changes, reverses, or abandons a
  decision, you must emit a replace or a drop for it; a stale entry is the worst failure.
- Distinguish user decisions from assistant proposals. Do not turn a proposal into a commitment: an assistant
  proposal the user has not accepted is not a decision. Moving on is not agreeing.
- Point to files instead of copying their content: name the file or artifact, don't paste it.
- Cite 1-2 item IDs exactly as they appear in the conversation (codex:<8 hex>#L<line>) for every add and replace.
  Never invent one.
- Only emit operations for what the new conversation changes. No operation for unchanged entries.
- Keep the whole ledger under the stated budget: when it is over, drop entries that no longer matter or merge
  related ones with replace.`

// ledgerSystemV2 fixes what the first round showed: the v1 ledger filled with
// progress notes and open questions, hit its cap within 9-13 compactions, and
// silent eviction then decided what it kept. v2 keeps progress out and, over
// budget, asks the model to merge or drop (consolidate) before any eviction.
const ledgerSystemV2 = ledgerSystem + `

What never goes in the ledger (it belongs to the other half and is rewritten there every compaction):
- progress, status, milestones, completed or in-flight work, commits, test results ("M4 is complete", "slices 1-3
  landed", "the sheet was rebuilt");
- open questions, pending approvals, and next steps;
- one-off instructions for a single task ("commit this", "fix 5-8").
A decision is a choice that constrains future work and would still matter weeks later. When in doubt, leave it out.`

const consolidateSystem = `You keep the ledger of one long-running conversation: decisions in force, things tried or rejected, and the
user's standing preferences. It is over its size budget. Return edit operations, as JSON, that bring it under budget.
Code applies them.

- replace: merge related entries into one. Reuse the key of one of them, state the merged text fully (keep exact
  values), keep the strongest who and user_words, and cite 1-2 of the merged entries' cites. Then drop the others.
- drop: an entry that is progress or status, an open question, a one-off instruction, or superseded by another
  entry. Give the reason.

Merge before you drop. Never drop a decision that is still in force only because it is old: old decisions are what
this ledger is for. Fill unused fields with "" or [].`

// consolidate asks the model to merge or drop entries until the ledger is
// under budget. Eviction by code stays as a last resort (enforceCap).
func consolidate(ctx context.Context, l *Ledger, step int, cut time.Time, valid map[string]bool) error {
	for try := 0; try < 2 && len(l.Render()) > ledgerBudget; try++ {
		cur := l.Render()
		var out struct {
			Ops []ledgerOp `json:"ops"`
		}
		prompt := fmt.Sprintf("<ledger chars=%d budget=%d>\n%s\n</ledger>", len(cur), ledgerBudget*3/4, cur)
		req := llm.Request{Model: llm.Reader, System: consolidateSystem, Prompt: prompt, Schema: ledgerSchema, Timeout: 20 * time.Minute}
		if _, err := llm.JSONRequest(ctx, req, &out); err != nil {
			return fmt.Errorf("consolidate: %w", err)
		}
		for i := range out.Ops {
			if out.Ops[i].Op == "drop" && out.Ops[i].Reason == "" {
				out.Ops[i].Reason = "consolidated"
			}
		}
		l.Ops += len(out.Ops)
		l.apply(out.Ops, step, cut.UTC().Format("2006-01-02"), valid, ledgerFieldChars)
		fmt.Printf("consolidate step %d: %d → %d chars, %d ops\n", step, len(cur), len(l.Render()), len(out.Ops))
	}
	return nil
}

const ledgerSchema = `{"type":"object","properties":{"ops":{"type":"array","items":{"type":"object","properties":{"op":{"type":"string","enum":["add","replace","drop"]},"kind":{"type":"string","enum":["decision","tried","preference"]},"key":{"type":"string"},"text":{"type":"string"},"why":{"type":"string"},"who":{"type":"string"},"user_words":{"type":"string"},"date":{"type":"string"},"reason":{"type":"string"},"cites":{"type":"array","items":{"type":"string"}}},"required":["op","kind","key","text","why","who","user_words","date","reason","cites"]}}},"required":["ops"]}`

const nowSystem = `You are the assistant in the conversation below, and your context is about to be compacted. Write the "Now" half
of the continuation summary: what a future assistant needs to pick the work up exactly where it stands. The other
half, the ledger of decisions, rejected ideas and standing preferences, is carried separately and shown below; don't
repeat it. The latest user message and your last reply are copied verbatim by code, so don't quote them in full.

Sections (Markdown headings, in this order):
## Where the work stands
What is done, what is in flight, and which files or artifacts are involved (name them; don't copy their content).
## Next step
The exact next action, in line with the user's latest request. If the user has not approved it, say it is a proposal.
## Open questions and waiting on
Unanswered questions, pending approvals and their limits, delegated or running work, and what we are waiting on.

Rules:
- End every line with the item ID(s) it comes from, in brackets, exactly as they appear (codex:<8 hex>#L<line>).
- Distinguish user decisions from assistant proposals or assumptions. Do not turn proposals into commitments,
  reports into verified results, partial checks into verified correctness, or historical evidence into current proof.
- Stay under %d characters.`

// TwoPartSummary returns the cached two-part summary at the trial's last real
// compaction and when that compaction happened.
// ledger is "none" (Now only), "v1", "v2", or "record" / "record2" (Now only in the
// prompt; the record is searched with spin decisions). Every version reads the same Now parts.
func TwoPartSummary(s corpus.Session, t Trial, windowChars int, ledger string) (string, time.Time, error) {
	_, since, err := recentTail(s, t)
	if err != nil {
		return "", time.Time{}, err
	}
	if since.IsZero() {
		return "", since, nil // before the first compaction the tail is the whole conversation
	}
	dir := filepath.Join(twoPartCache, NativePrior(t))
	cuts, err := compactionsOf(NativePrior(t))
	if err != nil {
		return "", time.Time{}, err
	}
	k := indexOf(cuts, since)
	if k < 0 {
		return "", time.Time{}, fmt.Errorf("two-part: no compaction before %s", t.AskedAt)
	}
	now, err := os.ReadFile(nowPath(dir, k+1, windowChars))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("two-part summary not built for %s step %d (run spin eval two-part): %w", t.ID, k+1, err)
	}
	if ledger == "none" || IsRecord(ledger) { // the record is searched through spin, not shown
		return "# Now\n\n" + strings.TrimSpace(string(now)), since, nil
	}
	l, err := readLedger(ledgerPath(ledgerDir(dir, ledger), k+1))
	if err != nil {
		return "", time.Time{}, err
	}
	return renderTwoPart(string(now), l), since, nil
}

func renderTwoPart(now string, l Ledger) string {
	return "# Now\n\n" + strings.TrimSpace(now) + "\n\n# Ledger\n\nCarried forward across compactions; every change is an edit, " +
		"and a replaced entry shows what it replaced.\n\n" + l.Render()
}

// Render writes the ledger as Markdown, one section per kind.
func (l Ledger) Render() string {
	var b strings.Builder
	for _, k := range ledgerKinds {
		var keys []string
		for key, e := range l.Entries {
			if e.Kind == k.Kind {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "## %s\n", k.Title)
		if len(keys) == 0 {
			b.WriteString("(none)\n")
		}
		for _, key := range keys {
			e := l.Entries[key]
			fmt.Fprintf(&b, "- **%s** (%s): %s", key, orDash(e.Date), e.Text)
			if e.Why != "" {
				fmt.Fprintf(&b, " Why: %s", e.Why)
			}
			if e.Who != "" {
				fmt.Fprintf(&b, " By: %s", e.Who)
				if e.UserWords != "" {
					fmt.Fprintf(&b, ", %q", e.UserWords)
				}
				b.WriteString(".")
			} else if e.UserWords != "" {
				fmt.Fprintf(&b, " User: %q.", e.UserWords)
			}
			if e.Replaced != "" {
				fmt.Fprintf(&b, " Replaced: %s", e.Replaced)
			}
			if len(e.Cites) > 0 {
				fmt.Fprintf(&b, " [%s]", strings.Join(e.Cites, ", "))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// apply edits the ledger. It returns how many ops it could not apply.
// fieldCap clips text and why (0: no clip; the searchable record keeps full detail).
func (l *Ledger) apply(ops []ledgerOp, step int, date string, valid map[string]bool, fieldCap int) {
	for _, o := range ops {
		key := strings.TrimSpace(o.Key)
		if key == "" {
			l.Skipped++
			continue
		}
		var cites []string
		for _, c := range o.Cites {
			if valid[strings.TrimSpace(c)] {
				cites = append(cites, strings.TrimSpace(c))
			} else {
				l.BadCites++
			}
		}
		if len(cites) > 2 {
			cites = cites[:2]
		}
		old, exists := l.Entries[key]
		switch o.Op {
		case "add", "replace":
			text := clipField(o.Text, fieldCap)
			if text == "" {
				l.Skipped++
				continue
			}
			e := LedgerEntry{Kind: o.Kind, Text: text, Why: clipField(o.Why, fieldCap), Who: strings.TrimSpace(o.Who),
				UserWords: clipTo(o.UserWords, 300), Date: orDate(o.Date, date), Cites: cites, Step: step}
			if exists {
				if old.Text == text {
					e.Replaced = old.Replaced
				} else {
					e.Replaced = fmt.Sprintf("%q (%s)", clipField(old.Text, fieldCap/2), old.Date)
					l.Log = append(l.Log, LedgerChange{Step: step, Op: "replace", Key: key, Old: old.Text, New: text, Reason: o.Why})
				}
			}
			l.Entries[key] = e
		case "drop":
			if !exists {
				l.Skipped++
				continue
			}
			l.Log = append(l.Log, LedgerChange{Step: step, Op: "drop", Key: key, Old: old.Text, Reason: o.Reason})
			delete(l.Entries, key)
		default:
			l.Skipped++
		}
	}
}

// enforceCap evicts the least recently touched entries, rejected ideas first,
// while the ledger is over the hard cap. Every eviction is logged.
func (l *Ledger) enforceCap(step int) {
	for len(l.Render()) > ledgerHardCap && len(l.Entries) > 0 {
		victim, rank := "", func(e LedgerEntry) int {
			if e.Kind == "tried" {
				return 0
			}
			return 1
		}
		for k, e := range l.Entries {
			if victim == "" {
				victim = k
				continue
			}
			v := l.Entries[victim]
			if rank(e) < rank(v) || (rank(e) == rank(v) && (e.Step < v.Step || (e.Step == v.Step && k < victim))) {
				victim = k
			}
		}
		l.Log = append(l.Log, LedgerChange{Step: step, Op: "evict", Key: victim, Old: l.Entries[victim].Text, Reason: "over the hard cap"})
		delete(l.Entries, victim)
	}
}

// BuildTwoPart chains the ledger (version v1 or v2) across a Codex thread's
// real compaction points up to `until`, and writes the Now part at the
// compaction points in nowAt. Everything is cached; cached steps are reused.
// Now parts are shared by all ledger versions (they are written with the v1
// ledger in view), so ledger versions differ only in the ledger.
func BuildTwoPart(ctx context.Context, session string, until time.Time, nowAt []time.Time, windowChars, workers int, version string) error {
	system, fieldCap := ledgerSystem, ledgerFieldChars
	switch version {
	case "v2":
		system = ledgerSystemV2
	case "record":
		system, fieldCap = recordSystem, 0
	case "record2":
		system, fieldCap = recordSystemV2, 0
	}
	path, err := findCodexRollout(session)
	if err != nil {
		return err
	}
	s, _, err := corpus.ReadCodexSession(path)
	if err != nil {
		return err
	}
	cuts, err := codexCompactions(path)
	if err != nil {
		return err
	}
	valid := map[string]bool{}
	for _, it := range s.Items {
		valid[it.ID] = true
	}
	dir := filepath.Join(twoPartCache, session)
	ldir := ledgerDir(dir, version)
	if err := os.MkdirAll(ldir, 0o755); err != nil {
		return err
	}
	l := Ledger{Entries: map[string]LedgerEntry{}}
	var from time.Time
	for i, cut := range cuts {
		if cut.After(until) {
			break
		}
		step := i + 1
		p := ledgerPath(ldir, step)
		if cached, err := readLedger(p); err == nil {
			l, from = cached, cut
			continue
		}
		items := itemsBetween(s, from, cut)
		for _, chunk := range chunkItems(items, ledgerSpanChars) {
			var out struct {
				Ops []ledgerOp `json:"ops"`
			}
			cur := l.Render()
			prompt := fmt.Sprintf("<ledger chars=%d budget=%d>\n%s\n</ledger>\n\n<conversation_since>\n%s</conversation_since>",
				len(cur), ledgerBudget, cur, chunk)
			if IsRecord(version) {
				prompt = fmt.Sprintf("<record entries=%d>\n%s\n</record>\n\n<conversation_since>\n%s</conversation_since>", len(l.Entries), cur, chunk)
			}
			req := llm.Request{Model: llm.Reader, System: system, Prompt: prompt, Schema: ledgerSchema, Timeout: 20 * time.Minute}
			if _, err := llm.JSONRequest(ctx, req, &out); err != nil {
				return fmt.Errorf("ledger step %d: %w", step, err)
			}
			l.Ops += len(out.Ops)
			ops := out.Ops
			if version == "record2" {
				ops = l.gate(ops, step, userSaid(s, cut))
			}
			l.apply(ops, step, cut.UTC().Format("2006-01-02"), valid, fieldCap)
			if version == "record2" {
				if err := l.supersede(ctx, ops, step, userSaidBetween(s, from, cut)); err != nil {
					return fmt.Errorf("ledger step %d: %w", step, err)
				}
			}
			if version == "v2" && len(l.Render()) > ledgerBudget {
				if err := consolidate(ctx, &l, step, cut, valid); err != nil {
					return fmt.Errorf("ledger step %d: %w", step, err)
				}
			}
			if !IsRecord(version) { // the record is searched, not prompted, so it has no cap
				l.enforceCap(step)
			}
		}
		l.AsOf, l.Step = cut, step
		if err := writeJSON(p, l); err != nil {
			return err
		}
		fmt.Printf("ledger %d/%d as of %s: %d entries, %d chars, %d ops, %d changes logged\n",
			step, len(cuts), cut.Format(time.RFC3339), len(l.Entries), len(l.Render()), l.Ops, len(l.Log))
		l.Ops, l.Skipped, l.BadCites = 0, 0, 0
		from = cut
	}

	// Now parts are independent given the ledger, so they run in parallel.
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, max(workers, 1))
	for _, at := range nowAt {
		k := indexOf(cuts, at)
		if k < 0 {
			continue
		}
		step := k + 1
		p := nowPath(dir, step, windowChars)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if version != "v1" {
			return fmt.Errorf("Now part %d missing: build with --ledger v1 first", step)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			led, err := readLedger(ledgerPath(ledgerDir(dir, "v1"), step))
			if err == nil {
				var now string
				if now, err = writeNow(ctx, s, cuts[k], led, windowChars); err == nil {
					err = os.WriteFile(p, []byte(now+"\n"), 0o644)
					fmt.Printf("now %d as of %s: %d chars\n", step, cuts[k].Format(time.RFC3339), len(now))
				}
			}
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("now %d: %w", step, err)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func writeNow(ctx context.Context, s corpus.Session, cut time.Time, l Ledger, windowChars int) (string, error) {
	window := renderItems(s, time.Time{}, cut, corpus.RoleUser, corpus.RoleAssistant)
	if len(window) > windowChars {
		window = window[len(window)-windowChars:]
		if i := strings.Index(window, "\n\n["); i >= 0 {
			window = window[i+2:]
		}
	}
	prompt := "<ledger>\n" + l.Render() + "\n</ledger>\n\n<conversation>\n" + window + "</conversation>"
	// The model sometimes returns a cut-off or empty summary; retry until all three sections are present.
	var text string
	for try := 0; try < 3; try++ {
		res, err := llm.Run(ctx, llm.Request{Model: llm.Reader, System: fmt.Sprintf(nowSystem, nowBudget), Prompt: prompt, Timeout: 15 * time.Minute})
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(res.Text)
		if strings.Contains(text, "## Where the work stands") && strings.Contains(text, "## Next step") && strings.Contains(text, "## Open questions") {
			break
		}
		fmt.Printf("now as of %s: incomplete (%d chars), retrying\n", cut.Format(time.RFC3339), len(text))
	}
	return lastExchange(s, cut) + "\n\n" + text, nil
}

// lastExchange copies the latest user message before the cut and the
// assistant's last reply after it, verbatim (clipped), so the ask is never paraphrased.
func lastExchange(s corpus.Session, cut time.Time) string {
	var user, reply *corpus.Item
	for i := range s.Items {
		it := &s.Items[i]
		if !it.Time.Before(cut) {
			break
		}
		switch it.Role {
		case corpus.RoleUser:
			user, reply = it, nil
		case corpus.RoleAssistant:
			if user != nil {
				reply = it
			}
		}
	}
	var b strings.Builder
	b.WriteString("## Latest ask and last exchange (verbatim)\n")
	for _, it := range []*corpus.Item{user, reply} {
		if it != nil {
			fmt.Fprintf(&b, "[%s %s %s]\n%s\n\n", it.ID, it.Time.UTC().Format(time.RFC3339), it.Role, clipTo(it.Text, verbatimChars))
		}
	}
	return strings.TrimSpace(b.String())
}

// TwoPartCutoffs returns, for a trial set, each Codex session's latest cutoff
// and the compaction points the Now part is needed at.
func TwoPartCutoffs(trials []Trial) (map[string]time.Time, map[string][]time.Time, error) {
	until, nowAt := map[string]time.Time{}, map[string][]time.Time{}
	for _, t := range trials {
		id := NativePrior(t)
		if id == "" {
			continue
		}
		cuts, err := compactionsOf(id)
		if err != nil {
			return nil, nil, err
		}
		var since time.Time
		for _, c := range cuts {
			if c.Before(t.AskedAt) && c.After(since) {
				since = c
			}
		}
		if since.After(until[id]) {
			until[id] = since
		}
		if !since.IsZero() && !containsTime(nowAt[id], since) {
			nowAt[id] = append(nowAt[id], since)
		}
	}
	return until, nowAt, nil
}

func compactionsOf(session string) ([]time.Time, error) {
	path, err := findCodexRollout(session)
	if err != nil {
		return nil, err
	}
	return codexCompactions(path)
}

func indexOf(cuts []time.Time, at time.Time) int {
	for i, c := range cuts {
		if c.Equal(at) {
			return i
		}
	}
	return -1
}

func itemsBetween(s corpus.Session, from, to time.Time) []corpus.Item {
	var out []corpus.Item
	for _, it := range s.Items {
		if !it.Time.Before(from) && it.Time.Before(to) && (it.Role == corpus.RoleUser || it.Role == corpus.RoleAssistant) {
			out = append(out, it)
		}
	}
	return out
}

// chunkItems renders items into chunks of at most n characters, splitting only between items.
func chunkItems(items []corpus.Item, n int) []string {
	var chunks []string
	var b strings.Builder
	for _, it := range items {
		r := it.Render() + "\n\n"
		if b.Len() > 0 && b.Len()+len(r) > n {
			chunks = append(chunks, b.String())
			b.Reset()
		}
		b.WriteString(r)
	}
	if b.Len() > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

// ledgerDir keeps v1 ledgers where the first round wrote them.
func ledgerDir(dir, version string) string {
	if version == "v1" || version == "" {
		return dir
	}
	return filepath.Join(dir, version)
}

func ledgerPath(dir string, step int) string {
	return filepath.Join(dir, fmt.Sprintf("ledger-%03d.json", step))
}

func nowPath(dir string, step, window int) string {
	return filepath.Join(dir, fmt.Sprintf("now-%03d-%d.md", step, window))
}

func readLedger(p string) (Ledger, error) {
	var l Ledger
	b, err := os.ReadFile(p)
	if err != nil {
		return l, err
	}
	err = json.Unmarshal(b, &l)
	if l.Entries == nil {
		l.Entries = map[string]LedgerEntry{}
	}
	return l, err
}

func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func clipField(s string, n int) string {
	if n <= 0 {
		return strings.TrimSpace(s)
	}
	return clipTo(s, n)
}

func clipTo(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func orDate(d, fallback string) string {
	if strings.TrimSpace(d) == "" {
		return fallback
	}
	return d
}

func containsTime(ts []time.Time, t time.Time) bool {
	for _, x := range ts {
		if x.Equal(t) {
			return true
		}
	}
	return false
}
