// Package statuses implements scoped status sets with merge-by-key inheritance.
//
// Resolution algorithm for a list (pure; see Resolve):
//
//  1. Walk the ancestor chain root -> list: workspace, space, folder, list.
//  2. At each level apply that owner's status definitions, in any order:
//     - hidden=true            -> remove the key from the working set (if present)
//     - key already in the set -> override: every non-null attribute (name, color,
//     category, position) replaces the inherited value
//     - key not in the set     -> add a new status; it must be complete
//     (name+category+color). Incomplete rows for unknown
//     keys are orphaned overrides and are ignored.
//  3. Sort by (position, key). That ordered list is the kanban column set.
//
// Tasks store the stable status key. Keys are immutable; renaming changes name only.
// Every resolved set must contain >=1 not_started, active and done status; config
// mutations that would break this for any descendant list are rejected (422).
package statuses

import (
	"sort"

	"github.com/flowboard/backend/internal/hierarchy"
)

var Categories = []string{"not_started", "active", "done", "closed"}
var RequiredCategories = []string{"not_started", "active", "done"}

// Def is one stored row (status_defs).
type Def struct {
	ID       string   `json:"id"`
	OwnerID  string   `json:"ownerId"`
	Key      string   `json:"key"`
	Name     *string  `json:"name"`
	Category *string  `json:"category"`
	Color    *string  `json:"color"`
	Position *float64 `json:"position"`
	Hidden   bool     `json:"hidden"`
}

// Status is a resolved status as seen by a specific container.
type Status struct {
	ID           string  `json:"id"` // id of the definition that introduced the key
	Key          string  `json:"key"`
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	Color        string  `json:"color"`
	Position     float64 `json:"position"`
	OwnerType    string  `json:"ownerType"` // where the key was introduced
	OwnerID      string  `json:"ownerId"`
	OverriddenAt *string `json:"overriddenAt,omitempty"` // deepest owner that overrode an attribute
	Inherited    bool    `json:"inherited"`              // true when introduced above the resolving container
}

// Resolve computes the effective status set at the last element of chain.
func Resolve(chain []*hierarchy.Container, defs map[string][]Def) []Status {
	working := map[string]*Status{}
	leaf := ""
	if len(chain) > 0 {
		leaf = chain[len(chain)-1].ID
	}
	for _, owner := range chain {
		for _, d := range defs[owner.ID] {
			if d.Hidden {
				delete(working, d.Key)
				continue
			}
			if s, ok := working[d.Key]; ok {
				if d.Name != nil {
					s.Name = *d.Name
				}
				if d.Category != nil {
					s.Category = *d.Category
				}
				if d.Color != nil {
					s.Color = *d.Color
				}
				if d.Position != nil {
					s.Position = *d.Position
				}
				id := owner.ID
				s.OverriddenAt = &id
				continue
			}
			if d.Name == nil || d.Category == nil || d.Color == nil {
				continue // orphaned override (its base definition was removed)
			}
			pos := 0.0
			if d.Position != nil {
				pos = *d.Position
			}
			working[d.Key] = &Status{
				ID: d.ID, Key: d.Key, Name: *d.Name, Category: *d.Category, Color: *d.Color,
				Position: pos, OwnerType: owner.Type, OwnerID: owner.ID,
			}
		}
	}
	out := make([]Status, 0, len(working))
	for _, s := range working {
		s.Inherited = s.OwnerID != leaf
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// MissingCategories reports required categories absent from a resolved set.
func MissingCategories(set []Status) []string {
	have := map[string]bool{}
	for _, s := range set {
		have[s.Category] = true
	}
	var missing []string
	for _, c := range RequiredCategories {
		if !have[c] {
			missing = append(missing, c)
		}
	}
	return missing
}

func Find(set []Status, key string) *Status {
	for i := range set {
		if set[i].Key == key {
			return &set[i]
		}
	}
	return nil
}

// DefaultFor picks the status a task lands in when its current key is not
// available in a destination set: the first status with the same category,
// else the first not_started status. Sets are always valid, so this never fails.
func DefaultFor(set []Status, category string) *Status {
	for i := range set {
		if set[i].Category == category {
			return &set[i]
		}
	}
	for i := range set {
		if set[i].Category == "not_started" {
			return &set[i]
		}
	}
	if len(set) > 0 {
		return &set[0]
	}
	return nil
}
