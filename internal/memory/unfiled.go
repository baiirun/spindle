package memory

import "time"

// Adopt copies into p the unfiled entries whose Folder is folder or inside
// it, so what was learned before the folder was linked joins the project's
// log, timeline and next dream. SetProject calls it on every link.
//
// Ensures: an unfiled entry already adopted into p (some entry of p has it
// as Adopted) is skipped, so relinking adds nothing.
// Must not: change the unfiled log; another project linked to the same
// folder later adopts the same entries.
func (p Project) Adopt(folder string, now time.Time) (int, error) {
	panic("unimplemented")
}
