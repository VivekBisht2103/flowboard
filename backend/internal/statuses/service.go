package statuses

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/lib/pq"

	"github.com/flowboard/backend/internal/access"
	"github.com/flowboard/backend/internal/events"
	"github.com/flowboard/backend/internal/hierarchy"
	"github.com/flowboard/backend/internal/platform/db"
	"github.com/flowboard/backend/internal/platform/httpx"
	"github.com/flowboard/backend/internal/platform/ids"
)

var (
	keyRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)
	colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

type Service struct{ DB *sql.DB }

// LoadDefs loads definitions for the given owners (nil = all).
func LoadDefs(ctx context.Context, q db.Q, ownerIDs []string) (map[string][]Def, error) {
	query := `SELECT id, owner_id, key, name, category, color, position, hidden FROM status_defs`
	var args []any
	if ownerIDs != nil {
		query += ` WHERE owner_id = ANY($1)`
		args = append(args, pq.Array(ownerIDs))
	}
	query += ` ORDER BY owner_id, position NULLS LAST, key`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Def{}
	for rows.Next() {
		var d Def
		if err := rows.Scan(&d.ID, &d.OwnerID, &d.Key, &d.Name, &d.Category, &d.Color, &d.Position, &d.Hidden); err != nil {
			return nil, err
		}
		out[d.OwnerID] = append(out[d.OwnerID], d)
	}
	return out, rows.Err()
}

// ResolveFor resolves the status set at a container (normally a list).
func ResolveFor(ctx context.Context, q db.Q, t *hierarchy.Tree, containerID string) ([]Status, error) {
	chain := t.Chain(containerID)
	defs, err := LoadDefs(ctx, q, hierarchy.IDs(chain))
	if err != nil {
		return nil, err
	}
	return Resolve(chain, defs), nil
}

// View is the config screen payload for one container.
type View struct {
	ContainerID string   `json:"containerId"`
	Type        string   `json:"containerType"`
	Local       []Def    `json:"local"`     // rows owned by this container
	Inherited   []Status `json:"inherited"` // what the parent resolves to
	Resolved    []Status `json:"resolved"`  // what this container resolves to
}

func (s *Service) View(ctx context.Context, ev *access.Evaluator, id string) (*View, error) {
	c, err := ev.RequireView(id, "container")
	if err != nil {
		return nil, err
	}
	chain := ev.Tree.Chain(id)
	defs, err := LoadDefs(ctx, s.DB, hierarchy.IDs(chain))
	if err != nil {
		return nil, err
	}
	v := &View{ContainerID: c.ID, Type: c.Type, Local: defs[c.ID], Resolved: Resolve(chain, defs)}
	if v.Local == nil {
		v.Local = []Def{}
	}
	v.Inherited = Resolve(chain[:len(chain)-1], defs)
	return v, nil
}

type Input struct {
	Key      string   `json:"key"`
	Name     *string  `json:"name"`
	Category *string  `json:"category"`
	Color    *string  `json:"color"`
	Position *float64 `json:"position"`
	Hidden   *bool    `json:"hidden"`
	RemapTo  string   `json:"remapTo"`
}

// Effects describes side effects of a config change (task remaps).
type Effects struct {
	Remapped []Remap `json:"remapped"`
}

type Remap struct {
	ListID    string `json:"listId"`
	From      string `json:"from"`
	To        string `json:"to"`
	TaskCount int    `json:"taskCount"`
}

func validateAttrs(in Input) []httpx.FieldError {
	var errs []httpx.FieldError
	if in.Name != nil && (len(*in.Name) < 1 || len(*in.Name) > 60) {
		errs = append(errs, httpx.FieldError{Path: "name", Code: "INVALID_LENGTH", Message: "name must be 1-60 characters"})
	}
	if in.Category != nil && !slices.Contains(Categories, *in.Category) {
		errs = append(errs, httpx.FieldError{Path: "category", Code: "INVALID_ENUM", Message: "category must be one of not_started, active, done, closed"})
	}
	if in.Color != nil && !colorRe.MatchString(*in.Color) {
		errs = append(errs, httpx.FieldError{Path: "color", Code: "INVALID_FORMAT", Message: "color must be #RRGGBB"})
	}
	return errs
}

type mutation func(ctx context.Context, tx *sql.Tx, t *hierarchy.Tree, owner *hierarchy.Container) error

// mutate is the shared transaction shell for every status config change:
// lock -> authorise -> apply -> re-resolve every affected list -> validate
// invariants -> remap orphaned tasks or roll back.
func (s *Service) mutate(ctx context.Context, ev *access.Evaluator, ownerID, remapTo string, fn mutation) (*Effects, error) {
	if err := ev.RequireAdmin("change status configuration"); err != nil {
		return nil, err
	}
	var eff *Effects
	err := db.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if err := db.ConfigLock(ctx, tx, ev.Tree.Root.ID); err != nil {
			return err
		}
		t, err := hierarchy.LoadTree(ctx, tx)
		if err != nil {
			return err
		}
		owner := t.Nodes[ownerID]
		if owner == nil {
			return httpx.NotFound("container")
		}
		if err := fn(ctx, tx, t, owner); err != nil {
			return err
		}
		eff, err = validateAndRemap(ctx, tx, t, ev, owner, remapTo)
		return err
	})
	return eff, err
}

func validateAndRemap(ctx context.Context, tx *sql.Tx, t *hierarchy.Tree, ev *access.Evaluator, owner *hierarchy.Container, remapTo string) (*Effects, error) {
	defs, err := LoadDefs(ctx, tx, nil)
	if err != nil {
		return nil, err
	}
	type orphan struct {
		ListID, ListName, Key string
		Count                 int
		Set                   []Status
	}
	var invalid []map[string]any
	var orphans []orphan
	for _, l := range t.ListsUnder(owner.ID) {
		set := Resolve(t.Chain(l.ID), defs)
		if m := MissingCategories(set); len(m) > 0 {
			invalid = append(invalid, map[string]any{"listId": l.ID, "listName": l.Name, "missingCategories": m})
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT status_key, count(*) FROM tasks WHERE list_id = $1 AND deleted_at IS NULL GROUP BY status_key`, l.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return nil, err
			}
			if Find(set, k) == nil {
				orphans = append(orphans, orphan{l.ID, l.Name, k, n, set})
			}
		}
		rows.Close()
	}
	if len(invalid) > 0 {
		return nil, httpx.E(http.StatusUnprocessableEntity, "STATUS_SET_INVALID",
			"every list must keep at least one not_started, active and done status").With(map[string]any{"lists": invalid})
	}
	eff := &Effects{Remapped: []Remap{}}
	if len(orphans) == 0 {
		return eff, nil
	}
	if remapTo == "" {
		var inUse []map[string]any
		for _, o := range orphans {
			inUse = append(inUse, map[string]any{"listId": o.ListID, "listName": o.ListName, "statusKey": o.Key, "taskCount": o.Count})
		}
		return nil, httpx.Conflict("STATUS_IN_USE", "tasks still use this status; retry with remapTo=<statusKey> to move them").
			With(map[string]any{"inUse": inUse})
	}
	for _, o := range orphans {
		if Find(o.Set, remapTo) == nil {
			return nil, httpx.InvalidField("remapTo", "UNKNOWN_STATUS",
				fmt.Sprintf("status %q is not available in list %q after this change", remapTo, o.ListName))
		}
	}
	for _, o := range orphans {
		res, err := tx.ExecContext(ctx, `
			UPDATE tasks t SET status_key = $3, version = t.version + 1, updated_at = now(),
			       position = base.p + 1024 * s.rn
			FROM (SELECT id, row_number() OVER (ORDER BY position, id) rn FROM tasks
			      WHERE list_id = $1 AND status_key = $2 AND deleted_at IS NULL) s,
			     (SELECT COALESCE(max(position), 0) p FROM tasks
			      WHERE list_id = $1 AND status_key = $3 AND deleted_at IS NULL) base
			WHERE t.id = s.id`, o.ListID, o.Key, remapTo)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		eff.Remapped = append(eff.Remapped, Remap{ListID: o.ListID, From: o.Key, To: remapTo, TaskCount: int(n)})
		if err := events.Emit(ctx, tx, events.Event{
			Type: "status.remapped", WorkspaceID: t.Root.ID,
			Actor:   events.Actor{ID: ev.User.ID, Name: ev.User.Name},
			Changes: map[string]any{"listId": o.ListID, "from": o.Key, "to": remapTo, "taskCount": n},
			Summary: fmt.Sprintf("%s remapped %d task(s) in %s from %s to %s", ev.User.Name, n, o.ListName, o.Key, remapTo),
		}); err != nil {
			return nil, err
		}
	}
	return eff, nil
}

func localDef(ctx context.Context, q db.Q, ownerID, key string) (*Def, error) {
	var d Def
	err := q.QueryRowContext(ctx, `SELECT id, owner_id, key, name, category, color, position, hidden FROM status_defs WHERE owner_id = $1 AND key = $2`, ownerID, key).
		Scan(&d.ID, &d.OwnerID, &d.Key, &d.Name, &d.Category, &d.Color, &d.Position, &d.Hidden)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &d, err
}

func parentSet(ctx context.Context, q db.Q, t *hierarchy.Tree, owner *hierarchy.Container) ([]Status, error) {
	chain := t.Chain(owner.ID)
	defs, err := LoadDefs(ctx, q, hierarchy.IDs(chain))
	if err != nil {
		return nil, err
	}
	return Resolve(chain[:len(chain)-1], defs), nil
}

func nextPosition(ctx context.Context, q db.Q, t *hierarchy.Tree, owner *hierarchy.Container) (float64, error) {
	set, err := ResolveFor(ctx, q, t, owner.ID)
	if err != nil {
		return 0, err
	}
	max := 0.0
	for _, s := range set {
		if s.Position > max {
			max = s.Position
		}
	}
	return max + 1000, nil
}

// Create adds a local definition: a brand-new status, or an override of an inherited key.
func (s *Service) Create(ctx context.Context, ev *access.Evaluator, ownerID string, in Input) (*Def, *Effects, error) {
	if err := ev.RequireAdmin("change status configuration"); err != nil { // authorise before validating
		return nil, nil, err
	}
	if !keyRe.MatchString(in.Key) {
		return nil, nil, httpx.InvalidField("key", "INVALID_FORMAT", "key must match ^[a-z][a-z0-9_]{1,39}$")
	}
	if errs := validateAttrs(in); len(errs) > 0 {
		return nil, nil, httpx.Invalid(errs)
	}
	var out *Def
	eff, err := s.mutate(ctx, ev, ownerID, in.RemapTo, func(ctx context.Context, tx *sql.Tx, t *hierarchy.Tree, owner *hierarchy.Container) error {
		if existing, err := localDef(ctx, tx, owner.ID, in.Key); err != nil {
			return err
		} else if existing != nil {
			return httpx.Conflict("STATUS_EXISTS", "this container already defines key "+in.Key+"; use PATCH")
		}
		inherited, err := parentSet(ctx, tx, t, owner)
		if err != nil {
			return err
		}
		if Find(inherited, in.Key) == nil {
			var errs []httpx.FieldError
			for path, v := range map[string]*string{"name": in.Name, "category": in.Category, "color": in.Color} {
				if v == nil {
					errs = append(errs, httpx.FieldError{Path: path, Code: "REQUIRED", Message: path + " is required for a new status"})
				}
			}
			if len(errs) > 0 {
				slices.SortFunc(errs, func(a, b httpx.FieldError) int { return compare(a.Path, b.Path) })
				return httpx.Invalid(errs)
			}
			if in.Hidden != nil && *in.Hidden {
				return httpx.InvalidField("hidden", "NOT_INHERITED", "only inherited statuses can be hidden")
			}
		}
		pos := in.Position
		if pos == nil && Find(inherited, in.Key) == nil {
			p, err := nextPosition(ctx, tx, t, owner)
			if err != nil {
				return err
			}
			pos = &p
		}
		hidden := in.Hidden != nil && *in.Hidden
		d := Def{ID: ids.New("sts"), OwnerID: owner.ID, Key: in.Key, Name: in.Name, Category: in.Category, Color: in.Color, Position: pos, Hidden: hidden}
		_, err = tx.ExecContext(ctx, `INSERT INTO status_defs (id, owner_id, key, name, category, color, position, hidden) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			d.ID, d.OwnerID, d.Key, d.Name, d.Category, d.Color, d.Position, d.Hidden)
		out = &d
		return err
	})
	return out, eff, err
}

// Patch upserts the local row for key: edits a local status, or creates/edits an
// override (rename, recolor, re-categorise, reposition, hide) of an inherited one.
func (s *Service) Patch(ctx context.Context, ev *access.Evaluator, ownerID, key string, in Input) (*Def, *Effects, error) {
	if err := ev.RequireAdmin("change status configuration"); err != nil {
		return nil, nil, err
	}
	if errs := validateAttrs(in); len(errs) > 0 {
		return nil, nil, httpx.Invalid(errs)
	}
	var out *Def
	eff, err := s.mutate(ctx, ev, ownerID, in.RemapTo, func(ctx context.Context, tx *sql.Tx, t *hierarchy.Tree, owner *hierarchy.Container) error {
		local, err := localDef(ctx, tx, owner.ID, key)
		if err != nil {
			return err
		}
		inherited, err := parentSet(ctx, tx, t, owner)
		if err != nil {
			return err
		}
		isInherited := Find(inherited, key) != nil
		if local == nil && !isInherited {
			return httpx.NotFound("status")
		}
		if in.Hidden != nil && *in.Hidden && !isInherited {
			return httpx.InvalidField("hidden", "NOT_INHERITED", "only inherited statuses can be hidden; delete a local status instead")
		}
		if local == nil {
			local = &Def{ID: ids.New("sts"), OwnerID: owner.ID, Key: key}
			if _, err := tx.ExecContext(ctx, `INSERT INTO status_defs (id, owner_id, key) VALUES ($1,$2,$3)`, local.ID, owner.ID, key); err != nil {
				return err
			}
		}
		if in.Name != nil {
			local.Name = in.Name
		}
		if in.Category != nil {
			local.Category = in.Category
		}
		if in.Color != nil {
			local.Color = in.Color
		}
		if in.Position != nil {
			local.Position = in.Position
		}
		if in.Hidden != nil {
			local.Hidden = *in.Hidden
		}
		_, err = tx.ExecContext(ctx, `UPDATE status_defs SET name=$2, category=$3, color=$4, position=$5, hidden=$6, updated_at=now() WHERE id=$1`,
			local.ID, local.Name, local.Category, local.Color, local.Position, local.Hidden)
		out = local
		return err
	})
	return out, eff, err
}

// Delete removes the local row: deletes a local status, or reverts an override/hide.
// If tasks would be left on a status that no longer resolves, the call fails with
// 409 STATUS_IN_USE unless remapTo names a status valid in every affected list.
func (s *Service) Delete(ctx context.Context, ev *access.Evaluator, ownerID, key, remapTo string) (*Effects, error) {
	return s.mutate(ctx, ev, ownerID, remapTo, func(ctx context.Context, tx *sql.Tx, t *hierarchy.Tree, owner *hierarchy.Container) error {
		local, err := localDef(ctx, tx, owner.ID, key)
		if err != nil {
			return err
		}
		if local == nil {
			return httpx.NotFound("status")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM status_defs WHERE id = $1`, local.ID); err != nil {
			return err
		}
		// If the key no longer resolves here, descendant override/hide rows for it
		// are orphans; drop them so a future re-add does not silently pick them up.
		set, err := ResolveFor(ctx, tx, t, owner.ID)
		if err != nil {
			return err
		}
		if Find(set, key) == nil {
			desc := hierarchy.IDs(t.Subtree(owner.ID))
			_, err = tx.ExecContext(ctx, `DELETE FROM status_defs WHERE key = $1 AND owner_id = ANY($2)
				AND (hidden OR name IS NULL OR category IS NULL OR color IS NULL)`, key, pq.Array(desc))
		}
		return err
	})
}

// Reorder sets column order at this container. keys must be a permutation of the
// container's resolved keys; inherited keys get position-only override rows.
func (s *Service) Reorder(ctx context.Context, ev *access.Evaluator, ownerID string, keys []string) error {
	_, err := s.mutate(ctx, ev, ownerID, "", func(ctx context.Context, tx *sql.Tx, t *hierarchy.Tree, owner *hierarchy.Container) error {
		set, err := ResolveFor(ctx, tx, t, owner.ID)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, s := range set {
			have[s.Key] = true
		}
		seen := map[string]bool{}
		for _, k := range keys {
			if !have[k] || seen[k] {
				return httpx.InvalidField("keys", "INVALID_ORDER", "keys must list every resolved status exactly once")
			}
			seen[k] = true
		}
		if len(seen) != len(have) {
			return httpx.InvalidField("keys", "INVALID_ORDER", "keys must list every resolved status exactly once")
		}
		for i, k := range keys {
			pos := float64((i + 1) * 1000)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO status_defs (id, owner_id, key, position) VALUES ($1, $2, $3, $4)
				ON CONFLICT (owner_id, key) DO UPDATE SET position = EXCLUDED.position, updated_at = now()`,
				ids.New("sts"), owner.ID, k, pos); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
