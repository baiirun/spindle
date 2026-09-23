package corpus

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ChunkChars is the target size of a chunk. Chunk end times gate what an
// eval run may see, so smaller chunks mean less memory lost at each cutoff.
const ChunkChars = 40_000

// Chunk is a contiguous run of items from one session.
type Chunk struct {
	Source  string
	Session string
	Cwd     string
	Index   int
	Start   time.Time
	End     time.Time
	Items   []Item
	Path    string // set when read from disk
}

// SplitChunks cuts a session into chunks of about ChunkChars.
func SplitChunks(s Session) []Chunk {
	var chunks []Chunk
	cur := Chunk{Source: s.Source, Session: s.ID, Cwd: s.Cwd, Index: 1}
	size := 0
	for _, it := range s.Items {
		n := len(it.Render()) + 1
		if size > 0 && size+n > ChunkChars {
			chunks = append(chunks, cur)
			cur = Chunk{Source: s.Source, Session: s.ID, Cwd: s.Cwd, Index: cur.Index + 1}
			size = 0
		}
		cur.Items = append(cur.Items, it)
		size += n
	}
	if len(cur.Items) > 0 {
		chunks = append(chunks, cur)
	}
	for i := range chunks {
		chunks[i].Start = chunks[i].Items[0].Time
		chunks[i].End = chunks[i].Items[len(chunks[i].Items)-1].Time
	}
	return chunks
}

// SessionDir is where a session's chunks live under a corpus root.
func SessionDir(root, source, session string) string {
	return filepath.Join(root, source, session)
}

// Render returns the chunk file contents.
func (c Chunk) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\nsource: %s\nsession: %s\ncwd: %s\nchunk: %d\nstart: %s\nend: %s\n---\n",
		c.Source, c.Session, c.Cwd, c.Index, c.Start.UTC().Format(time.RFC3339), c.End.UTC().Format(time.RFC3339))
	for _, it := range c.Items {
		b.WriteString(it.Render())
		b.WriteByte('\n')
	}
	return b.String()
}

// Write stores the chunk, skipping the write when content is unchanged so
// file mtimes stay meaningful for caches.
func (c Chunk) Write(root string) (string, error) {
	dir := SessionDir(root, c.Source, c.Session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("%04d.md", c.Index))
	content := c.Render()
	if old, err := os.ReadFile(p); err == nil && string(old) == content {
		return p, nil
	}
	return p, os.WriteFile(p, []byte(content), 0o644)
}

// ReadChunkHeader parses a chunk's frontmatter without loading items.
func ReadChunkHeader(path string) (Chunk, error) {
	f, err := os.Open(path)
	if err != nil {
		return Chunk{}, err
	}
	defer f.Close()
	c := Chunk{Path: path}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	fences := 0
	for sc.Scan() {
		line := sc.Text()
		if line == "---" {
			fences++
			if fences == 2 {
				break
			}
			continue
		}
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch k {
		case "source":
			c.Source = v
		case "session":
			c.Session = v
		case "cwd":
			c.Cwd = v
		case "chunk":
			c.Index, _ = strconv.Atoi(v)
		case "start":
			c.Start, _ = time.Parse(time.RFC3339, v)
		case "end":
			c.End, _ = time.Parse(time.RFC3339, v)
		}
	}
	if fences < 2 {
		return Chunk{}, fmt.Errorf("%s: missing frontmatter", path)
	}
	return c, sc.Err()
}

// ChunkPaths lists every chunk file under a corpus root.
func ChunkPaths(root string) ([]string, error) {
	return filepath.Glob(filepath.Join(root, "*", "*", "*.md"))
}

var itemHeader = regexp.MustCompile(`^\[((?:codex|claude):\S+#L(\d+)) (\S+) (user|assistant|tool)\] ?`)

// ReadChunk loads a chunk with its items.
func ReadChunk(path string) (Chunk, error) {
	c, err := ReadChunkHeader(path)
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	body := string(data)
	if i := strings.Index(body[4:], "\n---\n"); i >= 0 {
		body = body[4+i+5:]
	}
	for _, line := range strings.Split(body, "\n") {
		if m := itemHeader.FindStringSubmatch(line); m != nil {
			t, _ := time.Parse(time.RFC3339, m[3])
			n, _ := strconv.Atoi(m[2])
			c.Items = append(c.Items, Item{ID: m[1], Line: n, Time: t, Role: Role(m[4]), Text: line[len(m[0]):]})
			continue
		}
		if len(c.Items) > 0 {
			last := &c.Items[len(c.Items)-1]
			last.Text += "\n" + line
		}
	}
	for i := range c.Items {
		c.Items[i].Text = strings.TrimRight(c.Items[i].Text, "\n")
	}
	return c, nil
}

// ReadSession loads all chunks of a session in order.
func ReadSession(root, source, session string) ([]Chunk, error) {
	paths, err := filepath.Glob(filepath.Join(SessionDir(root, source, session), "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var chunks []Chunk
	for _, p := range paths {
		c, err := ReadChunk(p)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, nil
}
