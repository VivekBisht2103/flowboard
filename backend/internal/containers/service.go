// Package containers is the application service for hierarchy CRUD, ordering,
// archive and grants. Reads are filtered by the access evaluator; writes are admin-only.
package containers

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/lib/pq"

	"github.com/flowboard/backend/internal/access"
	"github.com/flowboard/backend/internal/hierarchy"
	"github.com/flowboard/backend/internal/platform/db"
	"github.com/flowboard/backend/internal/platform/httpx"
	"github.com/flowboard/backend/internal/platform/ids"
)

type Service struct{ DB *sql.DB }

var prefix = map[string]string{hierarchy.Space: "spc", hierarchy.Folder: "fld", hierarchy.List: "lst"}

// Node is the tree API shape.
type Node struct {
	hierarchy.Container
	Children []*Node `json:"children"`
}

// Tree returns only nodes the caller can see. Because visibility requires a
// visible parent, filtering top-down never produces dangling children.
func (s *Service) Tree(ev *access.Evaluator) *Node {
	var build func(c *hierarchy.Container) *Node
	build = func(c *hierarchy.Container) *Node {
		n := &Node{Container: *c, Children: []*Node{}}
		for _, k := range ev.Tree.Children[c.ID] {
			if ev.CanView(k.ID) {
				n.Children = append(n.Children, build(k))
			}
		}
		return n
	}
	return build(ev.Tree.Root)
}

type Detail struct {
	hierarchy.Container
	Breadcrumbs []hierarchy.Container `json:"breadcrumbs"`
}

func (s *Service) Get(ev *access.Evaluator, id string) (*Detail, error) {
	c, err := ev.RequireView(id, "container")
	if err != nil {
		return nil, err
	}
	d := &Detail{Container: *c, Breadcrumbs: []hierarchy.Container{}}
	for _, a := range ev.Tree.Chain(id) {
		d.Breadcrumbs = append(d.Breadcrumbs, *a)
	}
	return d, nil
}

// Children is a cursor-paginated child listing ordered by (position, id).
func (s *Service) Children(ev *access.Evaluator, id string, limit int, cursor string) (*httpx.Page[hierarchy.Container], error) {
	if _, err := ev.RequireView(id, "container"); err != nil {
		return nil, err
	}
	c, err := httpx.DecodeCursor(cursor, "position")
	if err != nil {
		return nil, err
	}
	var visible []hierarchy.Container
	for _, k := range ev.Tree.Children[id] {
		if ev.CanView(k.ID) {
			visible = append(visible, *k)
		}
	}
	start := 0
	if c != nil {
		// Keyset: first sibling strictly after (position, id) of the cursor.
		var cp float64
		if _, err := fmt.Sscan(c.Value, &cp); err != nil {
			return nil, httpx.InvalidField("cursor", "INVALID_CURSOR", "malformed cursor")
		}
		start = len(visible)
		for i, k := range visible {
			if k.Position > cp || (k.Position == cp && k.ID > c.ID) {
				start = i
				break
			}
		}
	}
	end := min(start+limit, len(visible))
	page := &httpx.Page[hierarchy.Container]{Data: visible[start:end], Page: httpx.PageInfo{Limit: limit}}
	if page.Data == nil {
		page.Data = []hierarchy.Container{}
	}
	if end < len(visible) {
		last := visible[end-1]
		cur := httpx.EncodeCursor(httpx.Cursor{Value: fmt.Sprint(last.Position), ID: last.ID, Sort: "position"})
		page.Page.NextCursor = &cur
	}
	return page, nil
}

type CreateInput struct {
	ParentID   string `json:"parentId"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
}

func validName(n string) bool { return len(n) >= 1 && len(n) <= 200 }

func (s *Service) Create(ctx context.Context, ev *access.Evaluator, in CreateInput) (*hierarchy.Container, error) {
	if err := ev.RequireAdmin("create containers"); err != nil {
		return nil, err
	}
	var errs []httpx.FieldError
	want, ok := hierarchy.ValidParent[in.Type]
	if !ok {
		errs = append(errs, httpx.FieldError{Path: "type", Code: "INVALID_ENUM", Message: "type must be space, folder or list"})
	}
	if !validName(in.Name) {
		errs = append(errs, httpx.FieldError{Path: "name", Code: "INVALID_LENGTH", Message: "name must be 1-200 characters"})
	}
	if in.Visibility == "" {
		in.Visibility = "public"
	}
	if in.Visibility != "public" && in.Visibility != "private" {
		errs = append(errs, httpx.FieldError{Path: "visibility", Code: "INVALID_ENUM", Message: "visibility must be public or private"})
	}
	if len(errs) > 0 {
		return nil, httpx.Invalid(errs)
	}
	parent := ev.Tree.Nodes[in.ParentID]
	if parent == nil {
		return nil, httpx.InvalidField("parentId", "NOT_FOUND", "parent container not found")
	}
	if parent.Type != want {
		return nil, httpx.InvalidField("parentId", "INVALID_PARENT", fmt.Sprintf("a %s must be created under a %s, not a %s", in.Type, want, parent.Type))
	}
	c := &hierarchy.Container{ID: ids.New(prefix[in.Type]), WorkspaceID: parent.WorkspaceID, Type: in.Type, ParentID: &parent.ID,
		Name: in.Name, Visibility: in.Visibility, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	err := db.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if err := db.ConfigLock(ctx, tx, parent.WorkspaceID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(position), 0) + $2 FROM containers WHERE parent_id = $1 AND archived_at IS NULL`,
			parent.ID, ids.Step).Scan(&c.Position); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO containers (id, workspace_id, type, parent_id, name, position, visibility, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, c.ID, c.WorkspaceID, c.Type, c.ParentID, c.Name, c.Position, c.Visibility, c.CreatedAt)
		return err
	})
	return c, err
}

type PatchInput struct {
	Name       *string `json:"name"`
	Visibility *string `json:"visibility"`
}

func (s *Service) Patch(ctx context.Context, ev *access.Evaluator, id string, in PatchInput) (*hierarchy.Container, error) {
	if err := ev.RequireAdmin("edit containers"); err != nil {
		return nil, err
	}
	c := ev.Tree.Nodes[id]
	if c == nil {
		return nil, httpx.NotFound("container")
	}
	cp := *c
	if in.Name != nil {
		if !validName(*in.Name) {
			return nil, httpx.InvalidField("name", "INVALID_LENGTH", "name must be 1-200 characters")
		}
		cp.Name = *in.Name
	}
	if in.Visibility != nil {
		if *in.Visibility != "public" && *in.Visibility != "private" {
			return nil, httpx.InvalidField("visibility", "INVALID_ENUM", "visibility must be public or private")
		}
		if c.Type == hierarchy.Workspace {
			return nil, httpx.InvalidField("visibility", "NOT_ALLOWED", "the workspace is always visible to its members")
		}
		cp.Visibility = *in.Visibility
	}
	cp.UpdatedAt = time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, `UPDATE containers SET name=$2, visibility=$3, updated_at=$4 WHERE id=$1`, cp.ID, cp.Name, cp.Visibility, cp.UpdatedAt)
	return &cp, err
}

type MoveInput struct {
	BeforeID string `json:"beforeId"`
	AfterID  string `json:"afterId"`
}

// Move reorders a container among its siblings. Re-parenting is intentionally not
// supported in the MVP: it changes status/field resolution for every task below,
// so it needs the same validate-and-remap flow as status edits (week-2 item).
func (s *Service) Move(ctx context.Context, ev *access.Evaluator, id string, in MoveInput) (*hierarchy.Container, error) {
	if err := ev.RequireAdmin("reorder containers"); err != nil {
		return nil, err
	}
	c := ev.Tree.Nodes[id]
	if c == nil {
		return nil, httpx.NotFound("container")
	}
	if c.ParentID == nil {
		return nil, httpx.BadRequest("the workspace cannot be moved")
	}
	if in.BeforeID != "" && in.AfterID != "" {
		return nil, httpx.BadRequest("pass beforeId or afterId, not both")
	}
	cp := *c
	err := db.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if err := db.ConfigLock(ctx, tx, c.WorkspaceID); err != nil {
			return err
		}
		for attempt := 0; attempt < 2; attempt++ {
			items, err := siblingItems(ctx, tx, *c.ParentID, id)
			if err != nil {
				return err
			}
			prev, next, found := ids.Neighbors(items, in.BeforeID, in.AfterID)
			if !found {
				return httpx.InvalidField("beforeId", "NOT_SIBLING", "anchor must be a sibling of the moved container")
			}
			pos, ok := ids.Between(prev, next)
			if !ok {
				if _, err := tx.ExecContext(ctx, `UPDATE containers c SET position = s.rn * $2 FROM
					(SELECT id, row_number() OVER (ORDER BY position, id) rn FROM containers WHERE parent_id = $1 AND archived_at IS NULL) s
					WHERE c.id = s.id`, *c.ParentID, ids.Step); err != nil {
					return err
				}
				continue
			}
			cp.Position = pos
			_, err = tx.ExecContext(ctx, `UPDATE containers SET position = $2, updated_at = now() WHERE id = $1`, id, pos)
			return err
		}
		return fmt.Errorf("could not compute position")
	})
	return &cp, err
}

func siblingItems(ctx context.Context, q db.Q, parentID, exclude string) ([]ids.Item, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, position FROM containers WHERE parent_id = $1 AND id <> $2 AND archived_at IS NULL ORDER BY position, id`, parentID, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.Item
	for rows.Next() {
		var it ids.Item
		if err := rows.Scan(&it.ID, &it.Position); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Archive soft-deletes a container and its whole subtree with one timestamp.
// Tasks under archived lists stay in the table but are unreachable via the API.
func (s *Service) Archive(ctx context.Context, ev *access.Evaluator, id string) error {
	if err := ev.RequireAdmin("archive containers"); err != nil {
		return err
	}
	c := ev.Tree.Nodes[id]
	if c == nil {
		return httpx.NotFound("container")
	}
	if c.ParentID == nil {
		return httpx.BadRequest("the workspace cannot be archived")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE containers SET archived_at = now() WHERE id = ANY($1)`, pq.Array(hierarchy.IDs(ev.Tree.Subtree(id))))
	return err
}

// ---- Grants ----

func (s *Service) Grants(ctx context.Context, ev *access.Evaluator, id string) ([]access.Grant, error) {
	if err := ev.RequireAdmin("view grants"); err != nil {
		return nil, err
	}
	if ev.Tree.Nodes[id] == nil {
		return nil, httpx.NotFound("container")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT resource_id, user_id, mode FROM grants WHERE resource_id = $1 ORDER BY user_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []access.Grant{}
	for rows.Next() {
		var g access.Grant
		if err := rows.Scan(&g.ResourceID, &g.UserID, &g.Mode); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Service) PutGrant(ctx context.Context, ev *access.Evaluator, id, userID, mode string) (*access.Grant, error) {
	if err := ev.RequireAdmin("manage grants"); err != nil {
		return nil, err
	}
	c := ev.Tree.Nodes[id]
	if c == nil {
		return nil, httpx.NotFound("container")
	}
	if !slices.Contains([]string{hierarchy.Space, hierarchy.Folder, hierarchy.List}, c.Type) {
		return nil, httpx.BadRequest("grants attach to spaces, folders or lists")
	}
	if mode != "allow" && mode != "deny" {
		return nil, httpx.InvalidField("mode", "INVALID_ENUM", "mode must be allow or deny")
	}
	var exists bool
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, userID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, httpx.NotFound("user")
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO grants (resource_id, user_id, mode) VALUES ($1,$2,$3)
		ON CONFLICT (resource_id, user_id) DO UPDATE SET mode = EXCLUDED.mode`, id, userID, mode)
	return &access.Grant{ResourceID: id, UserID: userID, Mode: mode}, err
}

func (s *Service) DeleteGrant(ctx context.Context, ev *access.Evaluator, id, userID string) error {
	if err := ev.RequireAdmin("manage grants"); err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM grants WHERE resource_id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.NotFound("grant")
	}
	return nil
}
