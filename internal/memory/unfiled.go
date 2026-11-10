package memory

import (
	"fmt"
	"path/filepath"
	"time"
)

// Adopt copies into p the unfiled entries whose Folder is folder or inside
// it, so what was learned before the folder was linked joins the project's
// log, timeline and next dream. SetProject calls it on every link.
//
// Ensures: an unfiled entry already adopted into p (some entry of p has it
// as Adopted) is skipped, so relinking adds nothing.
// Must not: change the unfiled log; another project linked to the same
// folder later adopts the same entries.
func (p Project) Adopt(folder string, now time.Time) (int, error) {
	folder, err := filepath.Abs(folder)
	if err != nil {
		return 0, err
	}
	unfiled, err := Store(p.Home, UnfiledName).Entries()
	if err != nil || len(unfiled) == 0 {
		return 0, err
	}
	have, err := p.Entries()
	if err != nil {
		return 0, err
	}
	adopted := map[EntryID]bool{}
	for _, e := range have {
		if e.Adopted != "" {
			adopted[e.Adopted] = true
		}
	}
	var copies []Entry
	for _, e := range unfiled {
		if e.Folder == "" || adopted[e.ID] || !(within(e.Folder, folder) || within(resolve(e.Folder), resolve(folder))) {
			continue
		}
		c := e
		c.ID, c.Adopted = NewEntryID(now), e.ID
		copies = append(copies, c)
	}
	if err := p.Append(copies); err != nil {
		return 0, err
	}
	if len(copies) > 0 {
		p.Log(Event{At: now, Kind: "call", Name: "adopt", Note: fmt.Sprintf("%d unfiled entries from %s", len(copies), folder)})
	}
	return len(copies), nil
}
