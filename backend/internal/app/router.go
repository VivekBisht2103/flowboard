// Package app wires modules into the versioned HTTP API (/v1).
package app

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/flowboard/backend/internal/access"
	"github.com/flowboard/backend/internal/containers"
	"github.com/flowboard/backend/internal/fields"
	"github.com/flowboard/backend/internal/hierarchy"
	"github.com/flowboard/backend/internal/identity"
	"github.com/flowboard/backend/internal/platform/httpx"
	"github.com/flowboard/backend/internal/statuses"
	"github.com/flowboard/backend/internal/tasks"
	"github.com/flowboard/backend/openapi"
)

type App struct {
	DB         *sql.DB
	Identity   *identity.Service
	Containers *containers.Service
	Statuses   *statuses.Service
	Fields     *fields.Service
	Tasks      *tasks.Service
}

func New(d *sql.DB, idCfg identity.Config) *App {
	return &App{
		DB:         d,
		Identity:   &identity.Service{DB: d, Cfg: idCfg},
		Containers: &containers.Service{DB: d},
		Statuses:   &statuses.Service{DB: d},
		Fields:     &fields.Service{DB: d},
		Tasks:      &tasks.Service{DB: d},
	}
}

// scope loads the per-request permission snapshot (tree + caller's grants).
func (a *App) scope(r *http.Request) (*access.Evaluator, error) {
	t, err := hierarchy.LoadTree(r.Context(), a.DB)
	if err != nil {
		return nil, err
	}
	return access.Load(r.Context(), a.DB, identity.FromContext(r.Context()), t)
}

type H = httpx.Handler

func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(recoverer, logger, cors)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { httpx.WriteError(w, r, httpx.NotFound("route")) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.E(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed"))
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.DB.PingContext(ctx); err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db unavailable"})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/v1", func(r chi.Router) {
		r.Get("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write(openapi.Spec)
		})
		r.Method(http.MethodPost, "/auth/token", H(a.Identity.IssueHandler))

		r.Group(func(r chi.Router) {
			r.Use(a.Identity.Middleware)

			r.Method(http.MethodGet, "/me", H(a.me))
			r.Method(http.MethodGet, "/users", H(a.listUsers))
			r.Method(http.MethodGet, "/tree", H(a.tree))

			r.Method(http.MethodPost, "/containers", H(a.createContainer))
			r.Route("/containers/{id}", func(r chi.Router) {
				r.Method(http.MethodGet, "/", H(a.getContainer))
				r.Method(http.MethodPatch, "/", H(a.patchContainer))
				r.Method(http.MethodDelete, "/", H(a.archiveContainer))
				r.Method(http.MethodGet, "/children", H(a.children))
				r.Method(http.MethodPost, "/move", H(a.moveContainer))

				r.Method(http.MethodGet, "/grants", H(a.listGrants))
				r.Method(http.MethodPut, "/grants/{userId}", H(a.putGrant))
				r.Method(http.MethodDelete, "/grants/{userId}", H(a.deleteGrant))

				r.Method(http.MethodGet, "/statuses", H(a.statusView))
				r.Method(http.MethodPost, "/statuses", H(a.createStatus))
				r.Method(http.MethodPut, "/statuses/order", H(a.reorderStatuses))
				r.Method(http.MethodPatch, "/statuses/{key}", H(a.patchStatus))
				r.Method(http.MethodDelete, "/statuses/{key}", H(a.deleteStatus))

				r.Method(http.MethodGet, "/fields", H(a.fieldView))
				r.Method(http.MethodPost, "/fields", H(a.createField))
				r.Method(http.MethodPatch, "/fields/{fieldId}", H(a.patchField))
				r.Method(http.MethodDelete, "/fields/{fieldId}", H(a.deleteField))
			})

			r.Route("/lists/{id}", func(r chi.Router) {
				r.Method(http.MethodGet, "/resolved-statuses", H(a.resolvedStatuses))
				r.Method(http.MethodGet, "/resolved-fields", H(a.resolvedFields))
				r.Method(http.MethodGet, "/board", H(a.board))
				r.Method(http.MethodGet, "/tasks", H(a.listTasks))
				r.Method(http.MethodPost, "/tasks", H(a.createTask))
				r.Method(http.MethodGet, "/activity", H(a.listActivity))
			})

			r.Route("/tasks/{id}", func(r chi.Router) {
				r.Method(http.MethodGet, "/", H(a.getTask))
				r.Method(http.MethodPatch, "/", H(a.patchTask))
				r.Method(http.MethodDelete, "/", H(a.deleteTask))
				r.Method(http.MethodPost, "/move", H(a.moveTask))
				r.Method(http.MethodGet, "/activity", H(a.taskActivity))
			})
		})
	})
	return r
}

// ---- middleware ----

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "value", v, "stack", string(debug.Stack()))
				httpx.WriteError(w, r, httpx.E(http.StatusInternalServerError, "INTERNAL", "internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

func logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if r.URL.Path != "/healthz" {
			slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds(),
				"user", r.Header.Get("X-User-Id"))
		}
	})
}

// cors allows the Vite dev server; in Docker the SPA is same-origin behind nginx.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-User-Id")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
