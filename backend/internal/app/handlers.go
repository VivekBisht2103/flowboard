package app

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/flowboard/backend/internal/containers"
	"github.com/flowboard/backend/internal/events"
	"github.com/flowboard/backend/internal/fields"
	"github.com/flowboard/backend/internal/identity"
	"github.com/flowboard/backend/internal/platform/httpx"
	"github.com/flowboard/backend/internal/statuses"
	"github.com/flowboard/backend/internal/tasks"
)

func ok(w http.ResponseWriter, v any) error { httpx.WriteJSON(w, http.StatusOK, v); return nil }
func created(w http.ResponseWriter, v any) error {
	httpx.WriteJSON(w, http.StatusCreated, v)
	return nil
}
func noContent(w http.ResponseWriter) error { w.WriteHeader(http.StatusNoContent); return nil }

// ---- identity ----

func (a *App) me(w http.ResponseWriter, r *http.Request) error {
	return ok(w, identity.FromContext(r.Context()))
}

func (a *App) listUsers(w http.ResponseWriter, r *http.Request) error {
	us, err := a.Identity.ListUsers(r.Context())
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"data": us})
}

// ---- hierarchy ----

func (a *App) tree(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	return ok(w, a.Containers.Tree(ev))
}

func (a *App) getContainer(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	d, err := a.Containers.Get(ev, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return ok(w, d)
}

func (a *App) children(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	limit, err := httpx.Limit(r, 50, 200)
	if err != nil {
		return err
	}
	p, err := a.Containers.Children(ev, chi.URLParam(r, "id"), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	return ok(w, p)
}

func (a *App) createContainer(w http.ResponseWriter, r *http.Request) error {
	var in containers.CreateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	c, err := a.Containers.Create(r.Context(), ev, in)
	if err != nil {
		return err
	}
	return created(w, c)
}

func (a *App) patchContainer(w http.ResponseWriter, r *http.Request) error {
	var in containers.PatchInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	c, err := a.Containers.Patch(r.Context(), ev, chi.URLParam(r, "id"), in)
	if err != nil {
		return err
	}
	return ok(w, c)
}

func (a *App) moveContainer(w http.ResponseWriter, r *http.Request) error {
	var in containers.MoveInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	c, err := a.Containers.Move(r.Context(), ev, chi.URLParam(r, "id"), in)
	if err != nil {
		return err
	}
	return ok(w, c)
}

func (a *App) archiveContainer(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	if err := a.Containers.Archive(r.Context(), ev, chi.URLParam(r, "id")); err != nil {
		return err
	}
	return noContent(w)
}

func (a *App) listGrants(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	gs, err := a.Containers.Grants(r.Context(), ev, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"data": gs})
}

func (a *App) putGrant(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	g, err := a.Containers.PutGrant(r.Context(), ev, chi.URLParam(r, "id"), chi.URLParam(r, "userId"), in.Mode)
	if err != nil {
		return err
	}
	return ok(w, g)
}

func (a *App) deleteGrant(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	if err := a.Containers.DeleteGrant(r.Context(), ev, chi.URLParam(r, "id"), chi.URLParam(r, "userId")); err != nil {
		return err
	}
	return noContent(w)
}

// ---- statuses ----

func (a *App) statusView(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	v, err := a.Statuses.View(r.Context(), ev, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return ok(w, v)
}

func (a *App) createStatus(w http.ResponseWriter, r *http.Request) error {
	var in statuses.Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	d, eff, err := a.Statuses.Create(r.Context(), ev, chi.URLParam(r, "id"), in)
	if err != nil {
		return err
	}
	return created(w, map[string]any{"status": d, "effects": eff})
}

func (a *App) patchStatus(w http.ResponseWriter, r *http.Request) error {
	var in statuses.Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.RemapTo == "" {
		in.RemapTo = r.URL.Query().Get("remapTo")
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	d, eff, err := a.Statuses.Patch(r.Context(), ev, chi.URLParam(r, "id"), chi.URLParam(r, "key"), in)
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"status": d, "effects": eff})
}

func (a *App) deleteStatus(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	eff, err := a.Statuses.Delete(r.Context(), ev, chi.URLParam(r, "id"), chi.URLParam(r, "key"), r.URL.Query().Get("remapTo"))
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"effects": eff})
}

func (a *App) reorderStatuses(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Keys []string `json:"keys"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	if err := a.Statuses.Reorder(r.Context(), ev, chi.URLParam(r, "id"), in.Keys); err != nil {
		return err
	}
	return a.statusView(w, r)
}

func (a *App) resolvedStatuses(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	id := chi.URLParam(r, "id")
	if _, err := ev.RequireList(id); err != nil {
		return err
	}
	set, err := statuses.ResolveFor(r.Context(), a.DB, ev.Tree, id)
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"listId": id, "data": set})
}

// ---- fields ----

func (a *App) fieldView(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	v, err := a.Fields.View(r.Context(), ev, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return ok(w, v)
}

func (a *App) createField(w http.ResponseWriter, r *http.Request) error {
	var in fields.Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	d, eff, err := a.Fields.Create(r.Context(), ev, chi.URLParam(r, "id"), in)
	if err != nil {
		return err
	}
	return created(w, map[string]any{"field": d, "effects": eff})
}

func (a *App) patchField(w http.ResponseWriter, r *http.Request) error {
	var in fields.Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	d, eff, err := a.Fields.Patch(r.Context(), ev, chi.URLParam(r, "id"), chi.URLParam(r, "fieldId"), in)
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"field": d, "effects": eff})
}

func (a *App) deleteField(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	eff, err := a.Fields.Delete(r.Context(), ev, chi.URLParam(r, "id"), chi.URLParam(r, "fieldId"))
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"effects": eff})
}

func (a *App) resolvedFields(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	id := chi.URLParam(r, "id")
	if _, err := ev.RequireList(id); err != nil {
		return err
	}
	schema, err := fields.ResolveFor(r.Context(), a.DB, ev.Tree, id)
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"listId": id, "data": schema})
}

// ---- tasks ----

func (a *App) board(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	b, err := a.Tasks.Board(r.Context(), ev, chi.URLParam(r, "id"), r.URL.Query())
	if err != nil {
		return err
	}
	return ok(w, b)
}

func (a *App) listTasks(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	limit, err := httpx.Limit(r, 50, 200)
	if err != nil {
		return err
	}
	p, err := a.Tasks.List(r.Context(), ev, chi.URLParam(r, "id"), r.URL.Query(), limit)
	if err != nil {
		return err
	}
	return ok(w, p)
}

func (a *App) createTask(w http.ResponseWriter, r *http.Request) error {
	var in tasks.CreateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	t, err := a.Tasks.Create(r.Context(), ev, chi.URLParam(r, "id"), in)
	if err != nil {
		return err
	}
	return created(w, t)
}

func (a *App) getTask(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	t, err := a.Tasks.Get(r.Context(), ev, chi.URLParam(r, "id"))
	if err != nil {
		return err
	}
	return ok(w, t)
}

func (a *App) patchTask(w http.ResponseWriter, r *http.Request) error {
	var in tasks.UpdateInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	t, err := a.Tasks.Update(r.Context(), ev, chi.URLParam(r, "id"), in)
	if err != nil {
		return err
	}
	return ok(w, t)
}

func (a *App) deleteTask(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	if err := a.Tasks.Delete(r.Context(), ev, chi.URLParam(r, "id")); err != nil {
		return err
	}
	return noContent(w)
}

func (a *App) moveTask(w http.ResponseWriter, r *http.Request) error {
	var in tasks.MoveInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	res, err := a.Tasks.Move(r.Context(), ev, chi.URLParam(r, "id"), in)
	if err != nil {
		return err
	}
	return ok(w, res)
}

// ---- activity (eventually consistent read model) ----

func (a *App) listActivity(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	id := chi.URLParam(r, "id")
	if _, err := ev.RequireList(id); err != nil {
		return err
	}
	limit, err := httpx.Limit(r, 30, 100)
	if err != nil {
		return err
	}
	p, err := events.ListActivity(r.Context(), a.DB, "list_id", id, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	return ok(w, p)
}

func (a *App) taskActivity(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.scope(r)
	if err != nil {
		return err
	}
	id := chi.URLParam(r, "id")
	if _, err := a.Tasks.Get(r.Context(), ev, id); err != nil { // enforces list visibility
		return err
	}
	limit, err := httpx.Limit(r, 30, 100)
	if err != nil {
		return err
	}
	p, err := events.ListActivity(r.Context(), a.DB, "task_id", id, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	return ok(w, p)
}
