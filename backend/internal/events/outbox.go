// Package events implements the transactional outbox (producer side), the worker
// that drains it (consumer side), and the activity read model it builds.
//
// Delivery is at-least-once. Every consumer is idempotent on event_id.
package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flowboard/backend/internal/platform/db"
	"github.com/flowboard/backend/internal/platform/ids"
)

type Actor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TaskRef struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ListID    string `json:"listId"`
	StatusKey string `json:"statusKey"`
}

// Event is the versioned payload stored in the outbox and sent to webhooks.
type Event struct {
	SchemaVersion int            `json:"schemaVersion"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	OccurredAt    time.Time      `json:"occurredAt"`
	WorkspaceID   string         `json:"workspaceId"`
	Actor         Actor          `json:"actor"`
	Task          *TaskRef       `json:"task,omitempty"`
	Changes       map[string]any `json:"changes,omitempty"`
	Summary       string         `json:"summary"`
}

// Emit writes an event to the outbox. Call it with the same transaction that
// performs the domain change so the two commit (or roll back) together.
func Emit(ctx context.Context, q db.Q, e Event) error {
	if e.ID == "" {
		e.ID = ids.New("evt")
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	e.SchemaVersion = 1
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	agg := ""
	if e.Task != nil {
		agg = e.Task.ID
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO outbox (event_id, type, aggregate_id, payload) VALUES ($1, $2, $3, $4)`,
		e.ID, e.Type, agg, payload)
	return err
}
