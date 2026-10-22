// Command flowboard is the single backend binary with subcommands:
//
//	flowboard serve    run the HTTP API (stateless; scale horizontally)
//	flowboard worker   drain the event outbox (activity feed, webhooks, indexer stub)
//	flowboard migrate  apply schema migrations
//	flowboard seed     apply migrations and seed demo data (idempotent; --reset to wipe)
//	flowboard webhook-sink  demo receiver for outbound webhooks (verifies signatures)
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/flowboard/backend/internal/app"
	"github.com/flowboard/backend/internal/events"
	"github.com/flowboard/backend/internal/identity"
	"github.com/flowboard/backend/internal/platform/db"
	"github.com/flowboard/backend/internal/seed"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: flowboard serve|worker|migrate|seed [--reset]")
		os.Exit(2)
	}
	if os.Args[1] == "webhook-sink" {
		runWebhookSink()
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := db.Open(ctx, env("DATABASE_URL", "postgres://flowboard:flowboard@localhost:5432/flowboard?sslmode=disable"))
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer d.Close()

	switch os.Args[1] {
	case "migrate":
		must(db.Migrate(ctx, d))
		slog.Info("migrations applied")
	case "seed":
		must(db.Migrate(ctx, d))
		reset := len(os.Args) > 2 && os.Args[2] == "--reset"
		must(seed.Run(ctx, d, reset))
	case "serve":
		if envBool("MIGRATE_ON_START", false) {
			must(db.Migrate(ctx, d))
		}
		ttl, _ := time.ParseDuration(env("JWT_TTL", "15m"))
		a := app.New(d, identity.Config{
			JWTSecret:       []byte(env("JWT_SECRET", "dev-only-change-me")),
			TokenTTL:        ttl,
			AllowHeaderAuth: envBool("AUTH_ALLOW_HEADER", true),
			EnableDevIssuer: envBool("AUTH_DEV_ISSUER", true),
		})
		srv := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: a.Router(), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
		slog.Info("api listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			must(err)
		}
	case "worker":
		w := &events.Worker{DB: d, Consumers: []events.Consumer{
			events.ActivityProjector{},
			events.SearchIndexer{},
			events.Webhook{URL: os.Getenv("WEBHOOK_URL"), Secret: []byte(env("WEBHOOK_SECRET", "dev-webhook-secret"))},
		}}
		must(w.Run(ctx))
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}

func must(err error) {
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// runWebhookSink is a stand-in third-party receiver: verifies the HMAC signature and logs events.
func runWebhookSink() {
	secret := []byte(env("WEBHOOK_SECRET", "dev-webhook-secret"))
	seen := map[string]bool{}
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		mac := hmac.New(sha256.New, secret)
		mac.Write(body)
		if !hmac.Equal([]byte(r.Header.Get("X-Flowboard-Signature")), []byte("sha256="+hex.EncodeToString(mac.Sum(nil)))) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		id := r.Header.Get("X-Flowboard-Event-Id")
		mu.Lock()
		dup := seen[id]
		seen[id] = true
		mu.Unlock()
		var e events.Event
		_ = json.Unmarshal(body, &e)
		slog.Info("webhook received", "event", id, "type", e.Type, "duplicate", dup, "summary", e.Summary)
		w.WriteHeader(http.StatusNoContent)
	})
	slog.Info("webhook sink listening", "addr", ":9090")
	must(http.ListenAndServe(":9090", mux))
}
