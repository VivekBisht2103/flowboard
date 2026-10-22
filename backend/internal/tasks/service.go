package tasks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"

	"github.com/flowboard/backend/internal/access"
	"github.com/flowboard/backend/internal/events"
	"github.com/flowboard/backend/internal/fields"
	"github.com/flowboard/backend/internal/hierarchy"
	"github.com/flowboard/backend/internal/platform/db"
	"github.com/flowboard/backend/internal/platform/httpx"
	"github.com/flowboard/backend/internal/platform/ids"
	"github.com/flowboard/backend/internal/statuses"
)

type Service struct{ DB *sql.DB }

// listConfig is everything needed to validate a task against a list.
type listConfig struct {
	List     *hierarchy.Container
	Statuses []statuses.Status
	Schema   []fields.Def
}

func loadConfig(ctx context.Context, q db.Q, ev *access.Evaluator, listID string) (*listConfig, error) {
	l, err := ev.RequireList(listID)
	if err != nil {
		return nil, err
	}
	sts, err := statuses.ResolveFor(ctx, q, ev.Tree, listID)
	if err != nil {
		return nil, err
	}
	schema, err := fields.ResolveFor(ctx, q, ev.Tree, listID)
	if err != nil {
		return nil, err
	}
	return &listConfig{List: l, Statuses: sts, Schema: schema}, nil
}

func (c *listConfig) present(t *Task) *Task {
	t.CustomFields = fields.Project(c.Schema, t.CustomFields)
	return t
}

func actor(ev *access.Evaluator) events.Actor {
	return events.Actor{ID: ev.User.ID, Name: ev.User.Name}
}

func ref(t *Task) *events.TaskRef {
	return &events.TaskRef{ID: t.ID, Title: t.Title, ListID: t.ListID, StatusKey: t.StatusKey}
}

func statusName(set []statuses.Status, key string) string {
	if s := statuses.Find(set, key); s != nil {
		return s.Name
	}
	return key
}

// ---- input validation shared by create/update ----

func validateTitle(title string, errs *[]httpx.FieldError) {
	n := utf8.RuneCountInString(strings.TrimSpace(title))
	if n < 1 || utf8.RuneCountInString(title) > 500 {
		*errs = append(*errs, httpx.FieldError{Path: "title", Code: "INVALID_LENGTH", Message: "title must be 1-500 characters"})
	}
}

func validatePriority(p string, errs *[]httpx.FieldError) {
	if !slices.Contains(Priorities, p) {
		*errs = append(*errs, httpx.FieldError{Path: "priority", Code: "INVALID_ENUM", Message: "priority must be one of urgent, high, normal, low, none"})
	}
}

func parseDue(raw json.RawMessage, errs *[]httpx.FieldError) (*time.Time, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*errs = append(*errs, httpx.FieldError{Path: "dueDate", Code: "INVALID_TYPE", Message: "dueDate must be an ISO-8601 string or null"})
		return nil, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u, true
		}
	}
	*errs = append(*errs, httpx.FieldError{Path: "dueDate", Code: "INVALID_FORMAT", Message: "dueDate must be RFC3339 (e.g. 2026-10-21T17:00:00Z) or YYYY-MM-DD"})
	return nil, false
}

func validateAssignees(ctx context.Context, q db.Q, in []string, errs *[]httpx.FieldError) []string {
	out := []string{}
	for _, a := range in {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return out
	}
	rows, err := q.QueryContext(ctx, `SELECT id FROM users WHERE id = ANY($1)`, pq.Array(out))
	if err != nil {
		*errs = append(*errs, httpx.FieldError{Path: "assigneeIds", Code: "LOOKUP_FAILED", Message: err.Error()})
		return out
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		found[id] = true
	}
	for i, a := range out {
		if !found[a] {
			*errs = append(*errs, httpx.FieldError{Path: fmt.Sprintf("assigneeIds[%d]", i), Code: "UNKNOWN_USER", Message: "unknown user " + a})
		}
	}
	return out
}

func unknownStatus(set []statuses.Status, key string) httpx.FieldError {
	keys := make([]string, len(set))
	for i, s := range set {
		keys[i] = s.Key
	}
	return httpx.FieldError{Path: "statusKey", Code: "UNKNOWN_STATUS",
		Message: fmt.Sprintf("status %q is not in this list's resolved set (%s)", key, strings.Join(keys, ", "))}
}

// ---- Get ----

func (s *Service) Get(ctx context.Context, ev *access.Evaluator, id string) (*Task, error) {
	t, err := getTask(ctx, s.DB, id, false)
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig(ctx, s.DB, ev, t.ListID)
	if err != nil {
		return nil, err
	}
	return cfg.present(t), nil
}

// ---- Create ----

type CreateInput struct {
	Title        string                     `json:"title"`
	Description  *string                    `json:"description"`
	StatusKey    *string                    `json:"statusKey"`
	Priority     *string                    `json:"priority"`
	AssigneeIDs  []string                   `json:"assigneeIds"`
	DueDate      json.RawMessage            `json:"dueDate"`
	CustomFields map[string]json.RawMessage `json:"customFields"`
}

func (s *Service) Create(ctx context.Context, ev *access.Evaluator, listID string, in CreateInput) (*Task, error) {
	cfg, err := loadConfig(ctx, s.DB, ev, listID)
	if err != nil {
		return nil, err
	}
	var errs []httpx.FieldError
	validateTitle(in.Title, &errs)
	t := &Task{ID: ids.New("tsk"), ListID: listID, Title: in.Title, Priority: "none", CreatedBy: &ev.User.ID}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.Priority != nil {
		validatePriority(*in.Priority, &errs)
		t.Priority = *in.Priority
	}
	// statusKey defaults to the first not_started status of the resolved set.
	if in.StatusKey != nil {
		if statuses.Find(cfg.Statuses, *in.StatusKey) == nil {
			errs = append(errs, unknownStatus(cfg.Statuses, *in.StatusKey))
		}
		t.StatusKey = *in.StatusKey
	} else {
		t.StatusKey = statuses.DefaultFor(cfg.Statuses, "not_started").Key
	}
	t.DueDate, _ = parseDue(in.DueDate, &errs)
	t.AssigneeIDs = validateAssignees(ctx, s.DB, in.AssigneeIDs, &errs)
	cf, ferrs := fields.Apply(cfg.Schema, map[string]any{}, in.CustomFields, true)
	errs = append(errs, ferrs...)
	if len(errs) > 0 {
		return nil, httpx.Invalid(errs)
	}
	t.CustomFields = cf
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt, t.Version = now, now, 1
	err = db.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if t.Position, err = appendPosition(ctx, tx, listID, t.StatusKey); err != nil {
			return err
		}
		cfb, _ := json.Marshal(t.CustomFields)
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (id, workspace_id, list_id, title, description, status_key, priority, assignee_ids,
			due_date, custom_fields, position, version, created_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,1,$12,$13,$13)`,
			t.ID, cfg.List.WorkspaceID, t.ListID, t.Title, t.Description, t.StatusKey, t.Priority, pq.Array(t.AssigneeIDs),
			t.DueDate, cfb, t.Position, t.CreatedBy, now); err != nil {
			return err
		}
		return events.Emit(ctx, tx, events.Event{Type: "task.created", WorkspaceID: cfg.List.WorkspaceID, Actor: actor(ev), Task: ref(t),
			Summary: fmt.Sprintf("%s created %q in %s", ev.User.Name, t.Title, statusName(cfg.Statuses, t.StatusKey))})
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ---- Update ----

type UpdateInput struct {
	Title        *string                    `json:"title"`
	Description  *string                    `json:"description"`
	StatusKey    *string                    `json:"statusKey"`
	Priority     *string                    `json:"priority"`
	AssigneeIDs  *[]string                  `json:"assigneeIds"`
	DueDate      json.RawMessage            `json:"dueDate"`
	CustomFields map[string]json.RawMessage `json:"customFields"`
	Version      *int                       `json:"version"` // optional optimistic-concurrency guard
}

func (s *Service) Update(ctx context.Context, ev *access.Evaluator, id string, in UpdateInput) (*Task, error) {
	var out *Task
	err := db.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		t, err := getTask(ctx, tx, id, true)
		if err != nil {
			return err
		}
		cfg, err := loadConfig(ctx, tx, ev, t.ListID)
		if err != nil {
			return err
		}
		if in.Version != nil && *in.Version != t.Version {
			return httpx.Conflict("VERSION_CONFLICT", "task was modified by someone else; reload and retry").
				With(map[string]any{"currentVersion": t.Version})
		}
		before := *t
		var errs []httpx.FieldError
		if in.Title != nil {
			validateTitle(*in.Title, &errs)
			t.Title = *in.Title
		}
		if in.Description != nil {
			t.Description = *in.Description
		}
		if in.Priority != nil {
			validatePriority(*in.Priority, &errs)
			t.Priority = *in.Priority
		}
		statusChanged := false
		if in.StatusKey != nil && *in.StatusKey != t.StatusKey {
			if statuses.Find(cfg.Statuses, *in.StatusKey) == nil {
				errs = append(errs, unknownStatus(cfg.Statuses, *in.StatusKey))
			}
			t.StatusKey, statusChanged = *in.StatusKey, true
		}
		if due, set := parseDue(in.DueDate, &errs); set {
			t.DueDate = due
		}
		if in.AssigneeIDs != nil {
			t.AssigneeIDs = validateAssignees(ctx, tx, *in.AssigneeIDs, &errs)
		}
		// Existing values are first projected onto the current schema so stale keys
		// never block an update; then the patch is applied and required fields checked.
		current := fields.Project(cfg.Schema, t.CustomFields)
		if len(in.CustomFields) > 0 {
			cf, ferrs := fields.Apply(cfg.Schema, current, in.CustomFields, true)
			errs = append(errs, ferrs...)
			t.CustomFields = cf
		}
		if len(errs) > 0 {
			return httpx.Invalid(errs)
		}
		if statusChanged {
			if t.Position, err = appendPosition(ctx, tx, t.ListID, t.StatusKey); err != nil {
				return err
			}
		}
		t.Version++
		t.UpdatedAt = time.Now().UTC()
		if err := saveTask(ctx, tx, t); err != nil {
			return err
		}
		changes := diff(&before, t)
		evType, summary := "task.updated", fmt.Sprintf("%s updated %q", ev.User.Name, t.Title)
		if statusChanged {
			evType = "task.status_changed"
			summary = fmt.Sprintf("%s moved %q from %s to %s", ev.User.Name, t.Title,
				statusName(cfg.Statuses, before.StatusKey), statusName(cfg.Statuses, t.StatusKey))
		}
		if len(changes) > 0 {
			if err := events.Emit(ctx, tx, events.Event{Type: evType, WorkspaceID: cfg.List.WorkspaceID, Actor: actor(ev), Task: ref(t),
				Changes: changes, Summary: summary}); err != nil {
				return err
			}
		}
		out = cfg.present(t)
		return nil
	})
	return out, err
}

func diff(a, b *Task) map[string]any {
	ch := map[string]any{}
	pair := func(k string, from, to any) { ch[k] = map[string]any{"from": from, "to": to} }
	if a.Title != b.Title {
		pair("title", a.Title, b.Title)
	}
	if a.Description != b.Description {
		ch["description"] = map[string]any{"changed": true}
	}
	if a.StatusKey != b.StatusKey {
		pair("statusKey", a.StatusKey, b.StatusKey)
	}
	if a.ListID != b.ListID {
		pair("listId", a.ListID, b.ListID)
	}
	if a.Priority != b.Priority {
		pair("priority", a.Priority, b.Priority)
	}
	if !slices.Equal(a.AssigneeIDs, b.AssigneeIDs) {
		pair("assigneeIds", a.AssigneeIDs, b.AssigneeIDs)
	}
	if (a.DueDate == nil) != (b.DueDate == nil) || (a.DueDate != nil && !a.DueDate.Equal(*b.DueDate)) {
		pair("dueDate", a.DueDate, b.DueDate)
	}
	ab, _ := json.Marshal(a.CustomFields)
	bb, _ := json.Marshal(b.CustomFields)
	if !bytes.Equal(ab, bb) {
		cf := map[string]any{}
		for k, v := range b.CustomFields {
			if ov, ok := a.CustomFields[k]; !ok || fmt.Sprint(ov) != fmt.Sprint(v) {
				cf[k] = map[string]any{"from": a.CustomFields[k], "to": v}
			}
		}
		for k, v := range a.CustomFields {
			if _, ok := b.CustomFields[k]; !ok {
				cf[k] = map[string]any{"from": v, "to": nil}
			}
		}
		ch["customFields"] = cf
	}
	return ch
}

// ---- Delete (soft) ----

func (s *Service) Delete(ctx context.Context, ev *access.Evaluator, id string) error {
	return db.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		t, err := getTask(ctx, tx, id, true)
		if err != nil {
			return err
		}
		l, err := ev.RequireList(t.ListID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET deleted_at = now(), version = version + 1 WHERE id = $1`, id); err != nil {
			return err
		}
		return events.Emit(ctx, tx, events.Event{Type: "task.deleted", WorkspaceID: l.WorkspaceID, Actor: actor(ev), Task: ref(t),
			Summary: fmt.Sprintf("%s deleted %q", ev.User.Name, t.Title)})
	})
}

// ---- Move: list change, status change and reorder in one command ----

type MoveInput struct {
	ListID       string                     `json:"listId"`
	StatusKey    string                     `json:"statusKey"`
	BeforeTaskID string                     `json:"beforeTaskId"`
	AfterTaskID  string                     `json:"afterTaskId"`
	StatusPolicy string                     `json:"statusPolicy"` // map_to_default (default) | reject
	CustomFields map[string]json.RawMessage `json:"customFields"`
	Version      *int                       `json:"version"`
}

type MoveEffects struct {
	StatusMapped   *StatusMap `json:"statusMapped"`
	StrippedFields []string   `json:"strippedFields"`
}

type StatusMap struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type MoveResult struct {
	Task    *Task       `json:"task"`
	Effects MoveEffects `json:"effects"`
}

// Move is the "drag card" command. Policy when the destination list does not
// resolve the task's current status:
//   - statusKey in the payload wins (must be valid in the destination);
//   - else statusPolicy=map_to_default (default): first destination status with the
//     same category, else the first not_started status;
//   - else statusPolicy=reject: 409 STATUS_NOT_IN_TARGET listing valid keys.
//
// Custom fields: values whose key is not in the destination schema are stripped
// (reported in effects.strippedFields); required destination fields must be present
// after the move, either already on the task or supplied in customFields.
func (s *Service) Move(ctx context.Context, ev *access.Evaluator, id string, in MoveInput) (*MoveResult, error) {
	if in.StatusPolicy == "" {
		in.StatusPolicy = "map_to_default"
	}
	if in.StatusPolicy != "map_to_default" && in.StatusPolicy != "reject" {
		return nil, httpx.InvalidField("statusPolicy", "INVALID_ENUM", "statusPolicy must be map_to_default or reject")
	}
	if in.BeforeTaskID != "" && in.AfterTaskID != "" {
		return nil, httpx.BadRequest("pass beforeTaskId or afterTaskId, not both")
	}
	res := &MoveResult{Effects: MoveEffects{StrippedFields: []string{}}}
	err := db.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		t, err := getTask(ctx, tx, id, true)
		if err != nil {
			return err
		}
		src, err := loadConfig(ctx, tx, ev, t.ListID)
		if err != nil {
			return err
		}
		if in.Version != nil && *in.Version != t.Version {
			return httpx.Conflict("VERSION_CONFLICT", "task was modified by someone else; reload and retry").
				With(map[string]any{"currentVersion": t.Version})
		}
		dst := src
		if in.ListID != "" && in.ListID != t.ListID {
			if dst, err = loadConfig(ctx, tx, ev, in.ListID); err != nil {
				return err
			}
		}
		before := *t

		// 1. status
		switch {
		case in.StatusKey != "":
			if statuses.Find(dst.Statuses, in.StatusKey) == nil {
				return httpx.Invalid([]httpx.FieldError{unknownStatus(dst.Statuses, in.StatusKey)})
			}
			t.StatusKey = in.StatusKey
		case statuses.Find(dst.Statuses, t.StatusKey) != nil:
			// keep
		case in.StatusPolicy == "reject":
			keys := []string{}
			for _, s := range dst.Statuses {
				keys = append(keys, s.Key)
			}
			return httpx.Conflict("STATUS_NOT_IN_TARGET",
				fmt.Sprintf("status %q does not exist in the destination list; pass statusKey or use statusPolicy=map_to_default", t.StatusKey)).
				With(map[string]any{"statusKey": t.StatusKey, "availableStatusKeys": keys})
		default:
			cat := "not_started"
			if old := statuses.Find(src.Statuses, t.StatusKey); old != nil {
				cat = old.Category
			}
			to := statuses.DefaultFor(dst.Statuses, cat)
			res.Effects.StatusMapped = &StatusMap{From: t.StatusKey, To: to.Key}
			t.StatusKey = to.Key
		}

		// 2. custom fields
		values := fields.Project(src.Schema, t.CustomFields)
		if dst != src {
			values, res.Effects.StrippedFields = fields.Retain(dst.Schema, values)
			if res.Effects.StrippedFields == nil {
				res.Effects.StrippedFields = []string{}
			}
		}
		if dst != src || len(in.CustomFields) > 0 {
			merged, ferrs := fields.Apply(dst.Schema, values, in.CustomFields, true)
			if len(ferrs) > 0 {
				return httpx.E(http.StatusBadRequest, "VALIDATION_FAILED",
					"task does not satisfy the destination list's custom fields; supply values in customFields").
					With(map[string]any{"fieldErrors": ferrs})
			}
			t.CustomFields = merged
		}
		t.ListID = dst.List.ID

		// 3. position within the destination column
		for attempt := 0; ; attempt++ {
			items, err := columnItems(ctx, tx, t.ListID, t.StatusKey, t.ID)
			if err != nil {
				return err
			}
			conv := make([]ids.Item, len(items))
			for i, it := range items {
				conv[i] = ids.Item{ID: it.ID, Position: it.Position}
			}
			prev, next, found := ids.Neighbors(conv, in.BeforeTaskID, in.AfterTaskID)
			if !found {
				return httpx.InvalidField("beforeTaskId", "NOT_IN_COLUMN", "anchor task must be in the destination column")
			}
			pos, ok := ids.Between(prev, next)
			if ok {
				t.Position = pos
				break
			}
			if attempt > 0 {
				return fmt.Errorf("position space exhausted after renumber")
			}
			if err := renumberColumn(ctx, tx, t.ListID, t.StatusKey); err != nil {
				return err
			}
		}

		t.Version++
		t.UpdatedAt = time.Now().UTC()
		if err := saveTask(ctx, tx, t); err != nil {
			return err
		}
		changes := diff(&before, t)
		switch {
		case before.ListID != t.ListID:
			err = events.Emit(ctx, tx, events.Event{Type: "task.moved", WorkspaceID: dst.List.WorkspaceID, Actor: actor(ev), Task: ref(t), Changes: changes,
				Summary: fmt.Sprintf("%s moved %q from %s to %s (%s)", ev.User.Name, t.Title, src.List.Name, dst.List.Name, statusName(dst.Statuses, t.StatusKey))})
		case before.StatusKey != t.StatusKey:
			err = events.Emit(ctx, tx, events.Event{Type: "task.status_changed", WorkspaceID: dst.List.WorkspaceID, Actor: actor(ev), Task: ref(t), Changes: changes,
				Summary: fmt.Sprintf("%s moved %q from %s to %s", ev.User.Name, t.Title, statusName(src.Statuses, before.StatusKey), statusName(dst.Statuses, t.StatusKey))})
		}
		if err != nil {
			return err
		}
		res.Task = dst.present(t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
