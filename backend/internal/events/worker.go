package events

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/flowboard/backend/internal/platform/db"
)

// Consumer handles one event. It must be idempotent (at-least-once delivery).
type Consumer interface {
	Name() string
	Handle(ctx context.Context, tx *sql.Tx, e *Event, raw []byte) error
}

// Worker drains the outbox: claim a batch with FOR UPDATE SKIP LOCKED (so N workers
// can run concurrently without double-processing), run every consumer, then mark
// processed. Failures back off exponentially; after MaxAttempts the row is marked
// dead for manual replay (UPDATE outbox SET dead=false, processed_at=NULL ...).
type Worker struct {
	DB           *sql.DB
	Consumers    []Consumer
	Batch        int
	PollInterval time.Duration
	MaxAttempts  int
}

func (w *Worker) Run(ctx context.Context) error {
	if w.Batch == 0 {
		w.Batch = 50
	}
	if w.PollInterval == 0 {
		w.PollInterval = 500 * time.Millisecond
	}
	if w.MaxAttempts == 0 {
		w.MaxAttempts = 8
	}
	slog.Info("worker started", "consumers", len(w.Consumers))
	for {
		n, err := w.Tick(ctx)
		if err != nil {
			slog.Error("worker tick failed", "err", err)
		}
		if n > 0 {
			continue // drain eagerly while there is work
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(w.PollInterval):
		}
	}
}

// Tick processes one batch and returns how many events it handled.
func (w *Worker) Tick(ctx context.Context) (int, error) {
	if w.MaxAttempts == 0 {
		w.MaxAttempts = 8
	}
	if w.Batch == 0 {
		w.Batch = 50
	}
	handled := 0
	err := db.InTx(ctx, w.DB, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, payload, attempts FROM outbox
			WHERE processed_at IS NULL AND available_at <= now()
			ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, w.Batch)
		if err != nil {
			return err
		}
		type item struct {
			id       int64
			raw      []byte
			attempts int
		}
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.raw, &it.attempts); err != nil {
				rows.Close()
				return err
			}
			items = append(items, it)
		}
		rows.Close()
		for _, it := range items {
			var e Event
			if err := json.Unmarshal(it.raw, &e); err != nil {
				_, _ = tx.ExecContext(ctx, `UPDATE outbox SET processed_at = now(), dead = true, last_error = $2 WHERE id = $1`, it.id, "undecodable: "+err.Error())
				continue
			}
			if herr := w.dispatch(ctx, tx, &e, it.raw); herr != nil {
				attempts := it.attempts + 1
				if attempts >= w.MaxAttempts {
					slog.Error("event dead-lettered", "event", e.ID, "type", e.Type, "err", herr)
					_, err = tx.ExecContext(ctx, `UPDATE outbox SET attempts=$2, processed_at=now(), dead=true, last_error=$3 WHERE id=$1`, it.id, attempts, herr.Error())
				} else {
					backoff := time.Duration(math.Pow(2, float64(attempts))) * time.Second
					slog.Warn("event delivery failed; will retry", "event", e.ID, "attempt", attempts, "in", backoff, "err", herr)
					_, err = tx.ExecContext(ctx, `UPDATE outbox SET attempts=$2, available_at=now() + $3::interval, last_error=$4 WHERE id=$1`,
						it.id, attempts, fmt.Sprintf("%d seconds", int(backoff.Seconds())), herr.Error())
				}
				if err != nil {
					return err
				}
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE outbox SET processed_at = now(), attempts = attempts + 1, last_error = NULL WHERE id = $1`, it.id); err != nil {
				return err
			}
			handled++
		}
		return nil
	})
	return handled, err
}

// dispatch runs all consumers inside a savepoint so a failing consumer rolls back
// only its own partial writes for this event.
func (w *Worker) dispatch(ctx context.Context, tx *sql.Tx, e *Event, raw []byte) error {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT evt`); err != nil {
		return err
	}
	for _, c := range w.Consumers {
		if err := c.Handle(ctx, tx, e, raw); err != nil {
			_, _ = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT evt`)
			return fmt.Errorf("%s: %w", c.Name(), err)
		}
	}
	_, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT evt`)
	return err
}

// ---- Consumers ----

// ActivityProjector builds the activity feed. Idempotent via UNIQUE(event_id).
type ActivityProjector struct{}

func (ActivityProjector) Name() string { return "activity" }

func (ActivityProjector) Handle(ctx context.Context, tx *sql.Tx, e *Event, raw []byte) error {
	var taskID, listID *string
	if e.Task != nil {
		taskID, listID = &e.Task.ID, &e.Task.ListID
	} else if l, ok := e.Changes["listId"].(string); ok {
		listID = &l
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO activity (event_id, type, task_id, list_id, actor_id, summary, payload, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (event_id) DO NOTHING`,
		e.ID, e.Type, taskID, listID, e.Actor.ID, e.Summary, raw, e.OccurredAt)
	return err
}

// SearchIndexer is a stub for a future search service: it logs what it would index.
type SearchIndexer struct{}

func (SearchIndexer) Name() string { return "search-indexer" }

func (SearchIndexer) Handle(_ context.Context, _ *sql.Tx, e *Event, _ []byte) error {
	if e.Task != nil {
		op := "upsert"
		if e.Type == "task.deleted" {
			op = "delete"
		}
		slog.Info("search-indexer (stub)", "op", op, "task", e.Task.ID, "event", e.ID)
	}
	return nil
}

// Webhook POSTs every event to a configured URL with an HMAC signature.
// Receivers dedupe on the X-Flowboard-Event-Id header.
type Webhook struct {
	URL    string
	Secret []byte
	Client *http.Client
}

func (Webhook) Name() string { return "webhook" }

func (h Webhook) Handle(ctx context.Context, _ *sql.Tx, e *Event, raw []byte) error {
	if h.URL == "" {
		slog.Debug("webhook (disabled)", "event", e.ID)
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, h.Secret)
	mac.Write(raw)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flowboard-Event-Id", e.ID)
	req.Header.Set("X-Flowboard-Event-Type", e.Type)
	req.Header.Set("X-Flowboard-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
