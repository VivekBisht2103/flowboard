package ids

// Step is the gap used when appending or renumbering positions.
const Step = 1024.0

// Item is an ordered sibling (task in a column, container under a parent).
type Item struct {
	ID       string
	Position float64
}

// Neighbors finds the positions around the drop target in an ordered sibling list
// that excludes the item being moved. beforeID = "place immediately before this
// item"; afterID = "place immediately after this item"; neither = append.
// found=false means the referenced anchor is not in the list.
func Neighbors(items []Item, beforeID, afterID string) (prev, next *float64, found bool) {
	if beforeID == "" && afterID == "" {
		if len(items) > 0 {
			p := items[len(items)-1].Position
			return &p, nil, true
		}
		return nil, nil, true
	}
	for i, it := range items {
		if beforeID != "" && it.ID == beforeID {
			n := it.Position
			if i > 0 {
				p := items[i-1].Position
				return &p, &n, true
			}
			return nil, &n, true
		}
		if afterID != "" && it.ID == afterID {
			p := it.Position
			if i+1 < len(items) {
				n := items[i+1].Position
				return &p, &n, true
			}
			return &p, nil, true
		}
	}
	return nil, nil, false
}

// Between returns a position strictly between prev and next (either may be nil
// meaning "start"/"end"). ok=false means the gap is exhausted and the caller
// should renumber the sibling set before retrying.
func Between(prev, next *float64) (pos float64, ok bool) {
	switch {
	case prev == nil && next == nil:
		return Step, true
	case prev == nil:
		return *next - Step, true
	case next == nil:
		return *prev + Step, true
	}
	if *next-*prev < 1e-6 {
		return 0, false
	}
	return (*prev + *next) / 2, true
}
