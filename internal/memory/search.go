package memory

// Search and zoom read every store; a folder's project only decides what a
// session starts with, never what it can find.

// SearchLayer is a layer memory_search can look in.
type SearchLayer string

const (
	SearchRules   SearchLayer = "rules"
	SearchWiki    SearchLayer = "wiki"
	SearchRollups SearchLayer = "rollups"
	SearchLog     SearchLayer = "log"
)

// Hit is one search result. Evidence holds the log entries behind it, so an
// answer built on it can be checked: a page's cited entries, a rollup's
// entries, or the entry itself.
type Hit struct {
	Store    string
	Layer    SearchLayer
	ID       string // a page ref, window id or entry id
	Line     string // a page's title, a rollup's line, an entry's gist
	Evidence []Entry
}

// Corpus is what search looks through: every store, loaded once.
type Corpus struct {
	Stores []StoreCorpus
}

// StoreCorpus is one store's layers.
type StoreCorpus struct {
	Name     string
	Entries  []Entry
	Rollups  []Rollup
	Rules    []Rule
	Wiki     []WikiPage
	Timeline []Node
}

// LoadCorpus reads every store.
func LoadCorpus(home string) (Corpus, error) {
	panic("unimplemented")
}

// Search ranks the corpus's items in layers (all four when empty) by how
// many query words they share, then by recency, and returns the first limit.
//
// Ensures: a page's hit carries at most 5 evidence entries, newest first.
func Search(c Corpus, query string, layers []SearchLayer, limit int) []Hit {
	panic("unimplemented")
}

// Zoom opens one id from the index: a window into its rollup line and its
// children's lines, an entry into its gist and pointer, or a page ref into
// the page.
//
// An id may be prefixed with its store ("spindle/d:2026-11-08"); without the
// prefix, an id several stores have (a day, a slug) opens in each, headed by
// the store's name.
//
// Ensures: an error naming the id when no store has it.
func Zoom(c Corpus, id string) (string, error) {
	panic("unimplemented")
}
