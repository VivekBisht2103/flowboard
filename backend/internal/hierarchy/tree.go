// Package hierarchy owns the container tree: workspace -> space -> folder -> list.
package hierarchy

import (
	"context"
	"sort"
	"time"

	"github.com/flowboard/backend/internal/platform/db"
)

const (
	Workspace = "workspace"
	Space     = "space"
	Folder    = "folder"
	List      = "list"
)

// ValidParent maps a child type to the only allowed parent type.
var ValidParent = map[string]string{Space: Workspace, Folder: Space, List: Folder}

type Container struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Type        string    `json:"type"`
	ParentID    *string   `json:"parentId"`
	Name        string    `json:"name"`
	Position    float64   `json:"position"`
	Visibility  string    `json:"visibility"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Tree is an in-memory snapshot of a workspace's live (non-archived) containers.
// At hobby scale the whole tree is loaded per request with one indexed query;
// see README "Scalability" for the materialised-path / cache plan.
type Tree struct {
	Root     *Container
	Nodes    map[string]*Container
	Children map[string][]*Container
}

const selectCols = `id, workspace_id, type, parent_id, name, position, visibility, created_at, updated_at`

func scan(row interface{ Scan(...any) error }) (*Container, error) {
	var c Container
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.Type, &c.ParentID, &c.Name, &c.Position, &c.Visibility, &c.CreatedAt, &c.UpdatedAt)
	return &c, err
}

// LoadTree loads the single workspace (single-tenant MVP) and all live containers.
func LoadTree(ctx context.Context, q db.Q) (*Tree, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+selectCols+` FROM containers WHERE archived_at IS NULL ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	t := &Tree{Nodes: map[string]*Container{}, Children: map[string][]*Container{}}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		t.Nodes[c.ID] = c
		if c.ParentID == nil {
			t.Root = c
		} else {
			t.Children[*c.ParentID] = append(t.Children[*c.ParentID], c)
		}
	}
	for _, kids := range t.Children {
		sort.SliceStable(kids, func(i, j int) bool {
			if kids[i].Position != kids[j].Position {
				return kids[i].Position < kids[j].Position
			}
			return kids[i].ID < kids[j].ID
		})
	}
	return t, rows.Err()
}

// Chain returns the path root..id (inclusive), or nil if id is unknown.
func (t *Tree) Chain(id string) []*Container {
	var rev []*Container
	for cur := t.Nodes[id]; cur != nil; {
		rev = append(rev, cur)
		if cur.ParentID == nil {
			break
		}
		cur = t.Nodes[*cur.ParentID]
	}
	if len(rev) == 0 {
		return nil
	}
	out := make([]*Container, len(rev))
	for i, c := range rev {
		out[len(rev)-1-i] = c
	}
	return out
}

// Subtree returns id and all its descendants (pre-order).
func (t *Tree) Subtree(id string) []*Container {
	var out []*Container
	var walk func(c *Container)
	walk = func(c *Container) {
		out = append(out, c)
		for _, k := range t.Children[c.ID] {
			walk(k)
		}
	}
	if n := t.Nodes[id]; n != nil {
		walk(n)
	}
	return out
}

// ListsUnder returns every list in the subtree of id (including id if it is a list).
func (t *Tree) ListsUnder(id string) []*Container {
	var out []*Container
	for _, c := range t.Subtree(id) {
		if c.Type == List {
			out = append(out, c)
		}
	}
	return out
}

// IDs is a helper for SQL "= ANY($1)" queries.
func IDs(cs []*Container) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}
