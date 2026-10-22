package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/flowboard/backend/internal/platform/httpx"
)

type Activity struct {
	ID        int64           `json:"id"`
	EventID   string          `json:"eventId"`
	Type      string          `json:"type"`
	TaskID    *string         `json:"taskId"`
	ListID    *string         `json:"listId"`
	ActorID   *string         `json:"actorId"`
	Summary   string          `json:"summary"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ListActivity reads the eventually-consistent feed, newest first, keyset on id.
func ListActivity(ctx context.Context, d *sql.DB, column, id string, limit int, cursor string) (*httpx.Page[Activity], error) {
	c, err := httpx.DecodeCursor(cursor, "id:desc")
	if err != nil {
		return nil, err
	}
	before := int64(1 << 62)
	if c != nil {
		if before, err = strconv.ParseInt(c.ID, 10, 64); err != nil {
			return nil, httpx.InvalidField("cursor", "INVALID_CURSOR", "malformed cursor")
		}
	}
	// column is chosen by the caller from a fixed set, never from user input.
	rows, err := d.QueryContext(ctx, `SELECT id, event_id, type, task_id, list_id, actor_id, summary, payload, created_at
		FROM activity WHERE `+column+` = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, id, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p := &httpx.Page[Activity]{Data: []Activity{}, Page: httpx.PageInfo{Limit: limit}}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.EventID, &a.Type, &a.TaskID, &a.ListID, &a.ActorID, &a.Summary, &a.Payload, &a.CreatedAt); err != nil {
			return nil, err
		}
		if len(p.Data) == limit {
			last := p.Data[len(p.Data)-1]
			s := httpx.EncodeCursor(httpx.Cursor{ID: strconv.FormatInt(last.ID, 10), Sort: "id:desc"})
			p.Page.NextCursor = &s
			break
		}
		p.Data = append(p.Data, a)
	}
	return p, rows.Err()
}
