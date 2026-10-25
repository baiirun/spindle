package memory

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// The decision record is the ledger half of the two-half design, kept out of
// the prompt and searched instead (docs/variants.md, "searchable decision
// record": best Eldspire recall). It has no budget, no eviction and no
// clipping. Every change is an add, replace or drop applied by code, and every
// replace and drop is logged, so what replaced what stays visible.
//
// The types and search mirror internal/eval's Ledger so the live record and
// the eval arm behave the same; eval keeps its own copy so cached eval
// results stay reproducible.

// Kinds of record entry, in render order.
var Kinds = []struct{ Kind, Title string }{
	{"decision", "Decisions in force"},
	{"tried", "Tried or rejected"},
	{"preference", "Standing preferences and corrections"},
}

func validKind(k string) bool {
	for _, x := range Kinds {
		if x.Kind == k {
			return true
		}
	}
	return false
}

// Entry is one record line.
type Entry struct {
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	Why       string    `json:"why,omitempty"`
	Who       string    `json:"who,omitempty"`
	UserWords string    `json:"user_words,omitempty"`
	Replaced  string    `json:"replaced,omitempty"` // set by code on replace
	Date      string    `json:"date"`
	Cites     []string  `json:"cites,omitempty"`
	Updated   time.Time `json:"updated"`
	Source    string    `json:"source,omitempty"` // "extract", "dream", or "tool"
}

// Change logs every replace and drop.
type Change struct {
	At     time.Time `json:"at"`
	Op     string    `json:"op"`
	Key    string    `json:"key"`
	Old    string    `json:"old"`
	New    string    `json:"new,omitempty"`
	Reason string    `json:"reason,omitempty"`
	Source string    `json:"source,omitempty"`
}

// Record is the project's decision record.
type Record struct {
	Entries map[string]Entry `json:"entries"`
	Log     []Change         `json:"log"`
	AsOf    time.Time        `json:"as_of"`
}

// Op is one edit, from the extraction model, the dream pass, or a tool call.
type Op struct {
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

// opsSchema is the structured output the extraction model returns.
const opsSchema = `{"type":"object","properties":{"ops":{"type":"array","items":{"type":"object","properties":{"op":{"type":"string","enum":["add","replace","drop"]},"kind":{"type":"string","enum":["decision","tried","preference"]},"key":{"type":"string"},"text":{"type":"string"},"why":{"type":"string"},"who":{"type":"string"},"user_words":{"type":"string"},"date":{"type":"string"},"reason":{"type":"string"},"cites":{"type":"array","items":{"type":"string"}}},"required":["op","kind","key","text","why","who","user_words","date","reason","cites"]}}},"required":["ops"]}`

// ApplyResult counts what Apply did.
type ApplyResult struct {
	Added, Replaced, Dropped, Skipped, BadCites int
}

// Apply edits the record. valid, when non-nil, is the set of item IDs a cite
// may name; others are dropped. source tags the entries and log lines.
func (r *Record) Apply(ops []Op, now time.Time, valid map[string]bool, source string) ApplyResult {
	if r.Entries == nil {
		r.Entries = map[string]Entry{}
	}
	var res ApplyResult
	date := now.UTC().Format("2006-01-02")
	for _, o := range ops {
		key := strings.TrimSpace(o.Key)
		if key == "" {
			res.Skipped++
			continue
		}
		var cites []string
		for _, c := range o.Cites {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if valid != nil && !valid[c] {
				res.BadCites++
				continue
			}
			cites = append(cites, c)
		}
		if len(cites) > 2 {
			cites = cites[:2]
		}
		old, exists := r.Entries[key]
		switch o.Op {
		case "add", "replace":
			text := strings.TrimSpace(o.Text)
			kind := strings.TrimSpace(o.Kind)
			if kind == "" && exists {
				kind = old.Kind
			}
			if text == "" || !validKind(kind) {
				res.Skipped++
				continue
			}
			e := Entry{Kind: kind, Text: text, Why: strings.TrimSpace(o.Why), Who: strings.TrimSpace(o.Who),
				UserWords: strings.TrimSpace(o.UserWords), Date: orDefault(strings.TrimSpace(o.Date), date),
				Cites: cites, Updated: now.UTC(), Source: source}
			if exists {
				if old.Text == text {
					e.Replaced = old.Replaced
				} else {
					e.Replaced = joinNonEmpty(fmt.Sprintf("%q (%s)", old.Text, old.Date), old.Replaced)
					r.Log = append(r.Log, Change{At: now.UTC(), Op: "replace", Key: key, Old: old.Text, New: text, Reason: e.Why, Source: source})
				}
				res.Replaced++
			} else {
				res.Added++
			}
			r.Entries[key] = e
		case "drop":
			if !exists {
				res.Skipped++
				continue
			}
			r.Log = append(r.Log, Change{At: now.UTC(), Op: "drop", Key: key, Old: old.Text, Reason: strings.TrimSpace(o.Reason), Source: source})
			delete(r.Entries, key)
			res.Dropped++
		default:
			res.Skipped++
		}
	}
	r.AsOf = now.UTC()
	return res
}

// RenderEntry writes one entry on one line, as the record and search show it.
func RenderEntry(key string, e Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** (%s): %s", key, orDefault(e.Date, "-"), e.Text)
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
	return b.String()
}

// Render writes the whole record as Markdown, one section per kind.
func (r Record) Render() string {
	var b strings.Builder
	for _, k := range Kinds {
		keys := r.keysOf(k.Kind)
		fmt.Fprintf(&b, "## %s\n", k.Title)
		if len(keys) == 0 {
			b.WriteString("(none)\n")
		}
		for _, key := range keys {
			b.WriteString("- " + RenderEntry(key, r.Entries[key]) + "\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func (r Record) keysOf(kind string) []string {
	var keys []string
	for key, e := range r.Entries {
		if e.Kind == kind {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// Recent returns keys ordered newest first by last update.
func (r Record) Recent() []string {
	keys := make([]string, 0, len(r.Entries))
	for k := range r.Entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := r.Entries[keys[i]], r.Entries[keys[j]]
		if !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
		return keys[i] < keys[j]
	})
	return keys
}

// History returns the log lines for a key, oldest first.
func (r Record) History(key string) []Change {
	var out []Change
	for _, c := range r.Log {
		if c.Key == key || c.New == key {
			out = append(out, c)
		}
	}
	return out
}

// Hit is one search result.
type Hit struct {
	Key     string
	Entry   Entry
	Score   float64
	Matched int  // distinct query terms that matched
	Rare    bool // a matched term occurs in at most rareDF entries
}

// Search ranks entries against the query by term overlap weighted by rarity
// (idf), with the key counted twice. Words are lightly stemmed, and terms of
// four or more letters also match words they prefix.
func (r Record) Search(query string, limit int) []Hit {
	docs := map[string]string{}
	for key, e := range r.Entries {
		docs[key] = strings.Join([]string{key, key, e.Text, e.Why, e.Who, e.UserWords, e.Replaced, e.Kind}, " ")
	}
	var hits []Hit
	for _, s := range rank(docs, query) {
		hits = append(hits, Hit{Key: s.key, Entry: r.Entries[s.key], Score: s.score, Matched: s.matched, Rare: s.rare})
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

type scored struct {
	key     string
	score   float64
	matched int
	rare    bool
}

// rareDF: a term in this few documents is specific enough to count on its own.
const rareDF = 3

// rank scores documents against a query (the record's search, shared by the
// user-message log). Documents with no matching term are left out.
func rank(docs map[string]string, query string) []scored {
	q := Terms(query)
	if len(q) == 0 {
		return nil
	}
	words := map[string][]string{}
	df := map[string]int{}
	for key, text := range docs {
		w := Terms(text)
		words[key] = w
		seen := map[string]bool{}
		for _, t := range q {
			if !seen[t] && matchesAny(t, w) {
				seen[t] = true
				df[t]++
			}
		}
	}
	n := float64(len(docs))
	var out []scored
	for key, w := range words {
		sc := scored{key: key}
		seen := map[string]bool{}
		for _, t := range q {
			tf := 0
			for _, x := range w {
				if termMatch(t, x) {
					tf++
				}
			}
			if tf > 0 {
				sc.score += math.Log(1+n/float64(df[t])) * (1 + math.Log(float64(tf)))
				if !seen[t] {
					seen[t] = true
					sc.matched++
				}
				if df[t] <= rareDF {
					sc.rare = true
				}
			}
		}
		if sc.score > 0 {
			out = append(out, sc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].key < out[j].key
	})
	return out
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields("the a an and or of to in on for with is are was were be been it its this that these those as at by from we you i our your did do does what which who why how when about any not no yes into than then there their they them he she his her has have had but if so can will would should could up out just also me my let lets ok okay go keep going please now next continue") {
		stopwords[w] = true
	}
}

// Terms splits text into lowercase, lightly stemmed search terms, without stopwords.
func Terms(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) > 1 && !stopwords[w] {
			out = append(out, stem(w))
		}
	}
	return out
}

// stem strips a common English suffix so "stressed", "stresses" and "stress" meet.
func stem(w string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 4 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

func termMatch(q, w string) bool { return w == q || (len(q) >= 4 && strings.HasPrefix(w, q)) }

func matchesAny(q string, words []string) bool {
	for _, w := range words {
		if termMatch(q, w) {
			return true
		}
	}
	return false
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}
