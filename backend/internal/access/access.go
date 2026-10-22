// Package access is the single place permission decisions are made.
//
// Model (MVP):
//   - Admins see and edit everything.
//   - Members: a container is visible iff its parent is visible AND it is allowed:
//     explicit grant on the node:  allow -> allowed, deny -> not allowed
//     no grant, public node:       allowed
//     no grant, private node:      allowed only if some ancestor carries an allow grant
//     Because visibility requires the parent to be visible, a deny on a space hides
//     every folder/list below it, and the tree API never leaks an orphaned child.
//   - Members may read/write tasks in any list they can see.
//   - Hierarchy, status and custom-field configuration and grants are admin-only.
package access

import (
	"context"

	"github.com/flowboard/backend/internal/hierarchy"
	"github.com/flowboard/backend/internal/identity"
	"github.com/flowboard/backend/internal/platform/db"
	"github.com/flowboard/backend/internal/platform/httpx"
)

type Grant struct {
	ResourceID string `json:"resourceId"`
	UserID     string `json:"userId"`
	Mode       string `json:"mode"`
}

type Evaluator struct {
	User   *identity.User
	Tree   *hierarchy.Tree
	grants map[string]string // resourceId -> mode for this user
	memo   map[string]bool
}

func NewEvaluator(u *identity.User, t *hierarchy.Tree, grants []Grant) *Evaluator {
	e := &Evaluator{User: u, Tree: t, grants: map[string]string{}, memo: map[string]bool{}}
	for _, g := range grants {
		if g.UserID == u.ID {
			e.grants[g.ResourceID] = g.Mode
		}
	}
	return e
}

// Load builds an evaluator for the user from the database.
func Load(ctx context.Context, q db.Q, u *identity.User, t *hierarchy.Tree) (*Evaluator, error) {
	rows, err := q.QueryContext(ctx, `SELECT resource_id, user_id, mode FROM grants WHERE user_id = $1`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gs []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.ResourceID, &g.UserID, &g.Mode); err != nil {
			return nil, err
		}
		gs = append(gs, g)
	}
	return NewEvaluator(u, t, gs), rows.Err()
}

func (e *Evaluator) CanView(id string) bool {
	n := e.Tree.Nodes[id]
	if n == nil {
		return false
	}
	if e.User.IsAdmin() {
		return true
	}
	if v, ok := e.memo[id]; ok {
		return v
	}
	var v bool
	switch {
	case n.ParentID == nil: // workspace root: every member sees it
		v = true
	case !e.CanView(*n.ParentID):
		v = false
	default:
		v = e.allowed(n)
	}
	e.memo[id] = v
	return v
}

func (e *Evaluator) allowed(n *hierarchy.Container) bool {
	if mode, ok := e.grants[n.ID]; ok {
		return mode == "allow"
	}
	if n.Visibility == "public" {
		return true
	}
	for cur := n; cur.ParentID != nil; {
		cur = e.Tree.Nodes[*cur.ParentID]
		if cur == nil {
			break
		}
		if e.grants[cur.ID] == "allow" {
			return true
		}
	}
	return false
}

// RequireView returns 404 for unknown ids and 403 for existing-but-hidden ones.
// (Returning 403 confirms existence; the spec asks for 403. Switching to 404 to
// avoid enumeration is a one-line change here.)
func (e *Evaluator) RequireView(id, what string) (*hierarchy.Container, error) {
	n := e.Tree.Nodes[id]
	if n == nil {
		return nil, httpx.NotFound(what)
	}
	if !e.CanView(id) {
		return nil, httpx.Forbidden("you do not have access to this " + what)
	}
	return n, nil
}

// RequireList is RequireView plus a type check.
func (e *Evaluator) RequireList(id string) (*hierarchy.Container, error) {
	n, err := e.RequireView(id, "list")
	if err != nil {
		return nil, err
	}
	if n.Type != hierarchy.List {
		return nil, httpx.NotFound("list")
	}
	return n, nil
}

// RequireAdmin guards configuration mutations.
func (e *Evaluator) RequireAdmin(action string) error {
	if !e.User.IsAdmin() {
		return httpx.Forbidden("only workspace admins can " + action)
	}
	return nil
}

// VisibleLists filters a set of list ids.
func (e *Evaluator) VisibleListIDs() []string {
	var out []string
	for id, n := range e.Tree.Nodes {
		if n.Type == hierarchy.List && e.CanView(id) {
			out = append(out, id)
		}
	}
	return out
}
