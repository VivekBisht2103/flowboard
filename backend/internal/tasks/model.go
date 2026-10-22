// Package tasks is the task application service: CRUD, status changes, moves
// between lists/columns, list queries and the board aggregate. Every write
// re-validates against the destination list's resolved statuses and field schema.
package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"

	"github.com/flowboard/backend/internal/platform/db"
	"github.com/flowboard/backend/internal/platform/httpx"
)

var Priorities = []string{"urgent", "high", "normal", "low", "none"}

type Task struct {
	ID           string         `json:"id"`
	ListID       string         `json:"listId"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	StatusKey    string         `json:"statusKey"`
	Priority     string         `json:"priority"`
	AssigneeIDs  []string       `json:"assigneeIds"`
	DueDate      *time.Time     `json:"dueDate"`
	CustomFields map[string]any `json:"customFields"`
	Position     float64        `json:"position"`
	Version      int            `json:"version"`
	CreatedBy    *string        `json:"createdBy"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

const taskCols = `t.id, t.list_id, t.title, t.description, t.status_key, t.priority, t.assignee_ids, t.due_date,
	t.custom_fields, t.position, t.version, t.created_by, t.created_at, t.updated_at`

func scanTask(row interface{ Scan(...any) error }, extra ...any) (*Task, error) {
	var t Task
	var cf []byte
	var assignees pq.StringArray
	dest := []any{&t.ID, &t.ListID, &t.Title, &t.Description, &t.StatusKey, &t.Priority, &assignees, &t.DueDate,
		&cf, &t.Position, &t.Version, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	t.AssigneeIDs = []string(assignees)
	if t.AssigneeIDs == nil {
		t.AssigneeIDs = []string{}
	}
	if err := json.Unmarshal(cf, &t.CustomFields); err != nil {
		return nil, err
	}
	if t.CustomFields == nil {
		t.CustomFields = map[string]any{}
	}
	if t.DueDate != nil {
		u := t.DueDate.UTC()
		t.DueDate = &u
	}
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	return &t, nil
}

func getTask(ctx context.Context, q db.Q, id string, lock bool) (*Task, error) {
	query := `SELECT ` + taskCols + ` FROM tasks t WHERE t.id = $1 AND t.deleted_at IS NULL`
	if lock {
		query += ` FOR UPDATE`
	}
	t, err := scanTask(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NotFound("task")
	}
	return t, err
}

func saveTask(ctx context.Context, q db.Q, t *Task) error {
	cf, err := json.Marshal(t.CustomFields)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE tasks SET list_id=$2, title=$3, description=$4, status_key=$5, priority=$6,
		assignee_ids=$7, due_date=$8, custom_fields=$9, position=$10, version=$11, updated_at=$12 WHERE id=$1`,
		t.ID, t.ListID, t.Title, t.Description, t.StatusKey, t.Priority, pq.Array(t.AssigneeIDs), t.DueDate, cf,
		t.Position, t.Version, t.UpdatedAt)
	return err
}

// columnItems returns the ordered tasks of one board column, excluding one task.
func columnItems(ctx context.Context, q db.Q, listID, statusKey, exclude string) ([]itemPos, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, position FROM tasks WHERE list_id=$1 AND status_key=$2 AND id<>$3 AND deleted_at IS NULL ORDER BY position, id`,
		listID, statusKey, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []itemPos
	for rows.Next() {
		var it itemPos
		if err := rows.Scan(&it.ID, &it.Position); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

type itemPos struct {
	ID       string
	Position float64
}

func renumberColumn(ctx context.Context, q db.Q, listID, statusKey string) error {
	_, err := q.ExecContext(ctx, `UPDATE tasks t SET position = s.rn * 1024 FROM
		(SELECT id, row_number() OVER (ORDER BY position, id) rn FROM tasks WHERE list_id=$1 AND status_key=$2 AND deleted_at IS NULL) s
		WHERE t.id = s.id`, listID, statusKey)
	return err
}

func appendPosition(ctx context.Context, q db.Q, listID, statusKey string) (float64, error) {
	var p float64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(max(position), 0) + 1024 FROM tasks WHERE list_id=$1 AND status_key=$2 AND deleted_at IS NULL`,
		listID, statusKey).Scan(&p)
	return p, err
}
