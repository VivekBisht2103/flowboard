package fields

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	optIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	ownerOK = []string{hierarchy.Space, hierarchy.Folder, hierarchy.List}
)

type Service struct{ DB *sql.DB }

func LoadDefs(ctx context.Context, q db.Q, ownerIDs []string) (map[string][]Def, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, owner_id, key, name, type, required, options, position
		FROM field_defs WHERE archived_at IS NULL AND owner_id = ANY($1) ORDER BY position, key`, pq.Array(ownerIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Def{}
	for rows.Next() {
		var d Def
		var opts []byte
		if err := rows.Scan(&d.ID, &d.OwnerID, &d.Key, &d.Name, &d.Type, &d.Required, &opts, &d.Position); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(opts, &d.Options); err != nil {
			return nil, err
		}
		if d.Options == nil {
			d.Options = []Option{}
		}
		out[d.OwnerID] = append(out[d.OwnerID], d)
	}
	return out, rows.Err()
}

// ResolveFor returns the resolved schema at a container.
func ResolveFor(ctx context.Context, q db.Q, t *hierarchy.Tree, containerID string) ([]Def, error) {
	chain := t.Chain(containerID)
	defs, err := LoadDefs(ctx, q, hierarchy.IDs(chain))
	if err != nil {
		return nil, err
	}
	return Resolve(chain, defs), nil
}

type View struct {
	ContainerID string `json:"containerId"`
	Type        string `json:"containerType"`
	Local       []Def  `json:"local"`
	Inherited   []Def  `json:"inherited"`
	Resolved    []Def  `json:"resolved"`
}

func (s *Service) View(ctx context.Context, ev *access.Evaluator, id string) (*View, error) {
	c, err := ev.RequireView(id, "container")
	if err != nil {
		return nil, err
	}
	resolved, err := ResolveFor(ctx, s.DB, ev.Tree, id)
	if err != nil {
		return nil, err
	}
	v := &View{ContainerID: c.ID, Type: c.Type, Local: []Def{}, Inherited: []Def{}, Resolved: resolved}
	for _, d := range resolved {
		if d.OwnerID == c.ID {
			v.Local = append(v.Local, d)
		} else {
			v.Inherited = append(v.Inherited, d)
		}
	}
	return v, nil
}

type Input struct {
	Key          *string         `json:"key"`
	Name         *string         `json:"name"`
	Type         *string         `json:"type"`
	Required     *bool           `json:"required"`
	Options      *[]Option       `json:"options"`
	Position     *float64        `json:"position"`
	DefaultValue json.RawMessage `json:"defaultValue"` // backfill for existing tasks when making a field required
}

type Effects struct {
	BackfilledTasks int `json:"backfilledTasks"`
	StrippedTasks   int `json:"strippedTasks"`
}

func validateOptions(typ string, opts *[]Option) []httpx.FieldError {
	if !IsSelect(typ) {
		if opts != nil && len(*opts) > 0 {
			return []httpx.FieldError{{Path: "options", Code: "NOT_ALLOWED", Message: "options are only valid for select fields"}}
		}
		return nil
	}
	if opts == nil || len(*opts) == 0 {
		return []httpx.FieldError{{Path: "options", Code: "REQUIRED", Message: "select fields need at least one option"}}
	}
	var errs []httpx.FieldError
	ids_, labels := map[string]bool{}, map[string]bool{}
	for i := range *opts {
		o := &(*opts)[i]
		p := fmt.Sprintf("options[%d]", i)
		if o.ID == "" {
			o.ID = ids.New("opt")
		} else if !optIDRe.MatchString(o.ID) {
			errs = append(errs, httpx.FieldError{Path: p + ".id", Code: "INVALID_FORMAT", Message: "option id must be lowercase alphanumeric/_/-"})
		}
		if o.Label == "" || len(o.Label) > 60 {
			errs = append(errs, httpx.FieldError{Path: p + ".label", Code: "INVALID_LENGTH", Message: "label must be 1-60 characters"})
		}
		if o.Color != "" && !colorRe.MatchString(o.Color) {
			errs = append(errs, httpx.FieldError{Path: p + ".color", Code: "INVALID_FORMAT", Message: "color must be #RRGGBB"})
		}
		if ids_[o.ID] {
			errs = append(errs, httpx.FieldError{Path: p + ".id", Code: "DUPLICATE", Message: "duplicate option id"})
		}
		if labels[o.Label] {
			errs = append(errs, httpx.FieldError{Path: p + ".label", Code: "DUPLICATE", Message: "duplicate option label"})
		}
		ids_[o.ID], labels[o.Label] = true, true
	}
	return errs
}

func (s *Service) inTx(ctx context.Context, ev *access.Evaluator, fn func(tx *sql.Tx, t *hierarchy.Tree) error) error {
	if err := ev.RequireAdmin("change custom field configuration"); err != nil {
		return err
	}
	return db.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if err := db.ConfigLock(ctx, tx, ev.Tree.Root.ID); err != nil {
			return err
		}
		t, err := hierarchy.LoadTree(ctx, tx)
		if err != nil {
			return err
		}
		return fn(tx, t)
	})
}

// backfill writes value into every task under owner that lacks key.
func backfill(ctx context.Context, tx *sql.Tx, t *hierarchy.Tree, owner string, d *Def, raw json.RawMessage) (int, error) {
	lists := hierarchy.IDs(t.ListsUnder(owner))
	var missing int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE list_id = ANY($1) AND deleted_at IS NULL AND NOT custom_fields ? $2`,
		pq.Array(lists), d.Key).Scan(&missing); err != nil {
		return 0, err
	}
	if missing == 0 {
		return 0, nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return 0, httpx.Conflict("FIELD_REQUIRED_BACKFILL",
			fmt.Sprintf("%d existing task(s) have no value for %q; provide defaultValue to backfill them", missing, d.Key)).
			With(map[string]any{"taskCount": missing})
	}
	v, errs := NormalizeValue(d, raw, "defaultValue")
	if len(errs) > 0 {
		return 0, httpx.Invalid(errs)
	}
	if v == nil {
		return 0, httpx.InvalidField("defaultValue", "REQUIRED", "defaultValue must not be empty")
	}
	b, _ := json.Marshal(map[string]any{d.Key: v})
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET custom_fields = custom_fields || $3::jsonb, version = version + 1, updated_at = now()
		WHERE list_id = ANY($1) AND deleted_at IS NULL AND NOT custom_fields ? $2`, pq.Array(lists), d.Key, string(b))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Service) Create(ctx context.Context, ev *access.Evaluator, ownerID string, in Input) (*Def, *Effects, error) {
	if err := ev.RequireAdmin("change custom field configuration"); err != nil { // authorise before validating
		return nil, nil, err
	}
	var errs []httpx.FieldError
	if in.Key == nil || !keyRe.MatchString(*in.Key) {
		errs = append(errs, httpx.FieldError{Path: "key", Code: "INVALID_FORMAT", Message: "key must match ^[a-z][a-z0-9_]{1,39}$"})
	}
	if in.Name == nil || len(*in.Name) < 1 || len(*in.Name) > 60 {
		errs = append(errs, httpx.FieldError{Path: "name", Code: "INVALID_LENGTH", Message: "name must be 1-60 characters"})
	}
	if in.Type == nil || !slices.Contains(Types, *in.Type) {
		errs = append(errs, httpx.FieldError{Path: "type", Code: "INVALID_ENUM", Message: "type must be one of text, number, single_select, multi_select, date, checkbox"})
	} else {
		errs = append(errs, validateOptions(*in.Type, in.Options)...)
	}
	if len(errs) > 0 {
		return nil, nil, httpx.Invalid(errs)
	}
	d := &Def{ID: ids.New("fld"), OwnerID: ownerID, Key: *in.Key, Name: *in.Name, Type: *in.Type, Required: in.Required != nil && *in.Required, Options: []Option{}}
	if in.Options != nil && IsSelect(d.Type) {
		d.Options = *in.Options
	}
	eff := &Effects{}
	err := s.inTx(ctx, ev, func(tx *sql.Tx, t *hierarchy.Tree) error {
		owner := t.Nodes[ownerID]
		if owner == nil {
			return httpx.NotFound("container")
		}
		if !slices.Contains(ownerOK, owner.Type) {
			return httpx.InvalidField("ownerId", "INVALID_OWNER", "custom fields can be defined on a space, folder or list")
		}
		d.OwnerType = owner.Type
		// Key must be unique along every root-to-leaf path through owner.
		scope := append(hierarchy.IDs(t.Chain(ownerID)), hierarchy.IDs(t.Subtree(ownerID))...)
		var clashOwner string
		err := tx.QueryRowContext(ctx, `SELECT owner_id FROM field_defs WHERE archived_at IS NULL AND key = $1 AND owner_id = ANY($2) LIMIT 1`,
			d.Key, pq.Array(scope)).Scan(&clashOwner)
		if err == nil {
			c := t.Nodes[clashOwner]
			return httpx.Conflict("FIELD_KEY_CONFLICT",
				fmt.Sprintf("key %q is already defined on %s %q in this branch", d.Key, c.Type, c.Name)).
				With(map[string]any{"key": d.Key, "conflictingOwnerId": clashOwner, "conflictingOwnerType": c.Type})
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if in.Position != nil {
			d.Position = *in.Position
		} else if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(position), 0) + 1000 FROM field_defs WHERE owner_id = $1 AND archived_at IS NULL`, ownerID).Scan(&d.Position); err != nil {
			return err
		}
		opts, _ := json.Marshal(d.Options)
		if _, err := tx.ExecContext(ctx, `INSERT INTO field_defs (id, owner_id, key, name, type, required, options, position) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			d.ID, d.OwnerID, d.Key, d.Name, d.Type, d.Required, opts, d.Position); err != nil {
			return err
		}
		if d.Required {
			n, err := backfill(ctx, tx, t, ownerID, d, in.DefaultValue)
			if err != nil {
				return err
			}
			eff.BackfilledTasks = n
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return d, eff, nil
}

func loadOwned(ctx context.Context, q db.Q, ownerID, fieldID string) (*Def, error) {
	defs, err := LoadDefs(ctx, q, []string{ownerID})
	if err != nil {
		return nil, err
	}
	for _, d := range defs[ownerID] {
		if d.ID == fieldID || d.Key == fieldID {
			d := d
			return &d, nil
		}
	}
	return nil, httpx.NotFound("custom field")
}

// Patch edits name/required/options/position. Key and type are immutable: changing
// a type would silently invalidate stored values, so it is modelled as delete+create.
func (s *Service) Patch(ctx context.Context, ev *access.Evaluator, ownerID, fieldID string, in Input) (*Def, *Effects, error) {
	var out *Def
	eff := &Effects{}
	err := s.inTx(ctx, ev, func(tx *sql.Tx, t *hierarchy.Tree) error {
		if t.Nodes[ownerID] == nil {
			return httpx.NotFound("container")
		}
		d, err := loadOwned(ctx, tx, ownerID, fieldID)
		if err != nil {
			return err
		}
		if in.Key != nil && *in.Key != d.Key {
			return httpx.InvalidField("key", "IMMUTABLE", "key cannot be changed")
		}
		if in.Type != nil && *in.Type != d.Type {
			return httpx.InvalidField("type", "IMMUTABLE", "type cannot be changed; create a new field instead")
		}
		if in.Name != nil {
			if len(*in.Name) < 1 || len(*in.Name) > 60 {
				return httpx.InvalidField("name", "INVALID_LENGTH", "name must be 1-60 characters")
			}
			d.Name = *in.Name
		}
		if in.Position != nil {
			d.Position = *in.Position
		}
		lists := hierarchy.IDs(t.ListsUnder(ownerID))
		if in.Options != nil {
			if errs := validateOptions(d.Type, in.Options); len(errs) > 0 {
				return httpx.Invalid(errs)
			}
			var removed []string
			for _, o := range d.Options {
				if !slices.ContainsFunc(*in.Options, func(n Option) bool { return n.ID == o.ID }) {
					removed = append(removed, o.ID)
				}
			}
			if len(removed) > 0 {
				var n int
				q := `SELECT count(*) FROM tasks WHERE list_id = ANY($1) AND deleted_at IS NULL AND custom_fields->>$2 = ANY($3)`
				if d.Type == "multi_select" {
					q = `SELECT count(*) FROM tasks WHERE list_id = ANY($1) AND deleted_at IS NULL AND custom_fields->$2 ?| $3`
				}
				if err := tx.QueryRowContext(ctx, q, pq.Array(lists), d.Key, pq.Array(removed)).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					return httpx.Conflict("OPTION_IN_USE", fmt.Sprintf("%d task(s) use option(s) being removed; change those tasks first", n)).
						With(map[string]any{"optionIds": removed, "taskCount": n})
				}
			}
			d.Options = *in.Options
		}
		if in.Required != nil && *in.Required && !d.Required {
			d.Required = true
			n, err := backfill(ctx, tx, t, ownerID, d, in.DefaultValue)
			if err != nil {
				return err
			}
			eff.BackfilledTasks = n
		} else if in.Required != nil {
			d.Required = *in.Required
		}
		opts, _ := json.Marshal(d.Options)
		_, err = tx.ExecContext(ctx, `UPDATE field_defs SET name=$2, required=$3, options=$4, position=$5, updated_at=now() WHERE id=$1`,
			d.ID, d.Name, d.Required, opts, d.Position)
		out = d
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	out.OwnerType = ev.Tree.Nodes[ownerID].Type
	return out, eff, nil
}

// Delete archives the definition and strips its values from every task in scope
// (policy: "strip"), in one transaction, so no task carries an orphaned value.
func (s *Service) Delete(ctx context.Context, ev *access.Evaluator, ownerID, fieldID string) (*Effects, error) {
	eff := &Effects{}
	err := s.inTx(ctx, ev, func(tx *sql.Tx, t *hierarchy.Tree) error {
		if t.Nodes[ownerID] == nil {
			return httpx.NotFound("container")
		}
		d, err := loadOwned(ctx, tx, ownerID, fieldID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE field_defs SET archived_at = now() WHERE id = $1`, d.ID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET custom_fields = custom_fields - $2, version = version + 1, updated_at = now()
			WHERE list_id = ANY($1) AND custom_fields ? $2`, pq.Array(hierarchy.IDs(t.ListsUnder(ownerID))), d.Key)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		eff.StrippedTasks = int(n)
		return events.Emit(ctx, tx, events.Event{
			Type: "field.deleted", WorkspaceID: t.Root.ID, Actor: events.Actor{ID: ev.User.ID, Name: ev.User.Name},
			Changes: map[string]any{"fieldKey": d.Key, "ownerId": ownerID, "strippedTasks": n},
			Summary: fmt.Sprintf("%s deleted custom field %s (%d task value(s) removed)", ev.User.Name, d.Name, n),
		})
	})
	return eff, err
}
