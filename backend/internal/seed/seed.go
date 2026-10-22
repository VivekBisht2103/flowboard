// Package seed creates the demo workspace. It is idempotent: it does nothing if
// users already exist, unless Reset is requested.
package seed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/lib/pq"

	"github.com/flowboard/backend/internal/platform/db"
)

const WorkspaceID = "ws_acme"

// Fixed, readable ids make the curl cookbook and tests easy to follow.
const (
	SpaceEng     = "spc_eng"
	SpaceMkt     = "spc_mkt"
	FolderQ2     = "fld_q2"
	FolderCamp   = "fld_campaigns"
	ListBacklog  = "lst_backlog"
	ListSprint   = "lst_sprint"
	ListSecurity = "lst_security"
	ListContent  = "lst_content"
)

func Run(ctx context.Context, d *sql.DB, reset bool) error {
	return db.InTx(ctx, d, func(tx *sql.Tx) error {
		if reset {
			if _, err := tx.ExecContext(ctx, `TRUNCATE activity, outbox, tasks, field_defs, status_defs, grants, containers, users`); err != nil {
				return err
			}
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			slog.Info("seed: data already present, skipping")
			return nil
		}
		return seed(ctx, tx)
	})
}

type exec struct {
	ctx context.Context
	tx  *sql.Tx
	err error
}

func (e *exec) do(q string, args ...any) {
	if e.err != nil {
		return
	}
	if _, err := e.tx.ExecContext(e.ctx, q, args...); err != nil {
		e.err = fmt.Errorf("%w: %s", err, q)
	}
}

func s(v string) *string   { return &v }
func f(v float64) *float64 { return &v }

func seed(ctx context.Context, tx *sql.Tx) error {
	e := &exec{ctx: ctx, tx: tx}

	// --- users ---
	for _, u := range [][4]string{
		{"priya", "Priya Sharma", "priya.sharma@flowboard.dev", "admin"},
		{"rahul", "Rahul Verma", "rahul.verma@flowboard.dev", "member"},
		{"ananya", "Ananya Iyer", "ananya.iyer@flowboard.dev", "member"},
	} {
		e.do(`INSERT INTO users (id, name, email, role) VALUES ($1,$2,$3,$4)`, u[0], u[1], u[2], u[3])
	}

	// --- hierarchy ---
	type c struct {
		id, typ, parent, name, vis string
		pos                        float64
	}
	for _, n := range []c{
		{WorkspaceID, "workspace", "", "Acme Inc.", "public", 0},
		{SpaceEng, "space", WorkspaceID, "Engineering", "public", 1024},
		{SpaceMkt, "space", WorkspaceID, "Marketing", "private", 2048},
		{FolderQ2, "folder", SpaceEng, "Q2 Launch", "public", 1024},
		{FolderCamp, "folder", SpaceMkt, "Campaigns", "public", 1024},
		{ListBacklog, "list", FolderQ2, "Backlog", "public", 1024},
		{ListSprint, "list", FolderQ2, "Sprint 12", "public", 2048},
		{ListSecurity, "list", FolderQ2, "Security Audit", "private", 3072},
		{ListContent, "list", FolderCamp, "Content Calendar", "public", 1024},
	} {
		var parent any
		if n.parent != "" {
			parent = n.parent
		}
		e.do(`INSERT INTO containers (id, workspace_id, type, parent_id, name, visibility, position) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			n.id, WorkspaceID, n.typ, parent, n.name, n.vis, n.pos)
	}

	// --- grants: Rahul and Ananya differ ---
	//   Rahul:   allowed into the private Marketing space.
	//   Ananya: allowed into the private Security Audit list; denied the public Backlog.
	e.do(`INSERT INTO grants (resource_id, user_id, mode) VALUES ($1,'rahul','allow'), ($2,'ananya','allow'), ($3,'ananya','deny')`,
		SpaceMkt, ListSecurity, ListBacklog)

	// --- statuses ---
	type st struct {
		id, owner, key   string
		name, cat, color *string
		pos              *float64
		hidden           bool
	}
	for _, x := range []st{
		// workspace defaults
		{"sts_ws_todo", WorkspaceID, "todo", s("To Do"), s("not_started"), s("#94a3b8"), f(1000), false},
		{"sts_ws_doing", WorkspaceID, "in_progress", s("In Progress"), s("active"), s("#3b82f6"), f(2000), false},
		{"sts_ws_done", WorkspaceID, "done", s("Done"), s("done"), s("#22c55e"), f(3000), false},
		// Engineering space adds a review column for every list below it
		{"sts_eng_review", SpaceEng, "in_review", s("In Review"), s("active"), s("#a855f7"), f(2500), false},
		// Sprint 12 list: rename To Do, hide In Review, add Blocked and QA
		{"sts_spr_todo", ListSprint, "todo", s("Ready"), nil, s("#0ea5e9"), nil, false},
		{"sts_spr_review", ListSprint, "in_review", nil, nil, nil, nil, true},
		{"sts_spr_blocked", ListSprint, "blocked", s("Blocked"), s("active"), s("#ef4444"), f(1500), false},
		{"sts_spr_qa", ListSprint, "qa", s("QA"), s("active"), s("#f59e0b"), f(2700), false},
		// Marketing space: its own vocabulary
		{"sts_mkt_todo", SpaceMkt, "todo", s("Idea"), nil, s("#f472b6"), nil, false},
		{"sts_mkt_sched", SpaceMkt, "scheduled", s("Scheduled"), s("active"), s("#14b8a6"), f(2500), false},
		{"sts_mkt_done", SpaceMkt, "done", s("Published"), nil, nil, nil, false},
	} {
		e.do(`INSERT INTO status_defs (id, owner_id, key, name, category, color, position, hidden) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			x.id, x.owner, x.key, x.name, x.cat, x.color, x.pos, x.hidden)
	}

	// --- custom fields (space, folder and list levels) ---
	type opt struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Color string `json:"color,omitempty"`
	}
	type fd struct {
		id, owner, key, name, typ string
		required                  bool
		options                   []opt
		pos                       float64
	}
	for _, x := range []fd{
		{"fld_eng_component", SpaceEng, "component", "Component", "single_select", true, []opt{
			{"frontend", "Frontend", "#3b82f6"}, {"backend", "Backend", "#22c55e"}, {"infra", "Infra", "#f97316"}}, 1000},
		{"fld_eng_points", SpaceEng, "story_points", "Story Points", "number", false, nil, 2000},
		{"fld_q2_release", FolderQ2, "target_release", "Target Release", "date", false, nil, 1000},
		{"fld_q2_flag", FolderQ2, "customer_facing", "Customer Facing", "checkbox", false, nil, 2000},
		{"fld_backlog_labels", ListBacklog, "labels", "Labels", "multi_select", false, []opt{
			{"bug", "Bug", "#ef4444"}, {"feature", "Feature", "#8b5cf6"}, {"chore", "Chore", "#64748b"}, {"tech_debt", "Tech Debt", "#eab308"}}, 1000},
		{"fld_mkt_channel", SpaceMkt, "channel", "Channel", "single_select", true, []opt{
			{"blog", "Blog", "#6366f1"}, {"social", "Social", "#ec4899"}, {"email", "Email", "#0ea5e9"}}, 1000},
		{"fld_mkt_budget", SpaceMkt, "budget", "Budget (USD)", "number", false, nil, 2000},
		{"fld_content_owner_note", ListContent, "brief", "Brief", "text", false, nil, 1000},
	} {
		opts := x.options
		if opts == nil {
			opts = []opt{}
		}
		b, _ := json.Marshal(opts)
		e.do(`INSERT INTO field_defs (id, owner_id, key, name, type, required, options, position) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			x.id, x.owner, x.key, x.name, x.typ, x.required, b, x.pos)
	}

	// --- tasks ---
	day := func(n int) *time.Time {
		t := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, n).Add(17 * time.Hour)
		return &t
	}
	type tk struct {
		list, title, status, prio string
		assignees                 []string
		due                       *time.Time
		cf                        map[string]any
	}
	tasks := []tk{
		// Backlog: todo, in_progress, in_review, done
		{ListBacklog, "Design onboarding checklist", "todo", "high", []string{"priya"}, day(5), map[string]any{"component": "frontend", "story_points": 5, "labels": []string{"feature"}, "customer_facing": true}},
		{ListBacklog, "Rate-limit public API", "todo", "urgent", []string{"rahul"}, day(2), map[string]any{"component": "backend", "story_points": 8, "labels": []string{"feature"}, "target_release": "2026-11-15"}},
		{ListBacklog, "Migrate CI runners to ARM", "todo", "low", []string{"ananya"}, nil, map[string]any{"component": "infra", "story_points": 3, "labels": []string{"chore"}}},
		{ListBacklog, "Fix flaky checkout test", "in_progress", "normal", []string{"rahul"}, day(1), map[string]any{"component": "frontend", "story_points": 2, "labels": []string{"bug", "tech_debt"}}},
		{ListBacklog, "Add OpenTelemetry tracing", "in_progress", "high", []string{"priya", "rahul"}, day(9), map[string]any{"component": "backend", "story_points": 8, "target_release": "2026-11-30"}},
		{ListBacklog, "Dark mode for settings", "in_review", "normal", []string{"ananya"}, day(3), map[string]any{"component": "frontend", "story_points": 3, "labels": []string{"feature"}, "customer_facing": true}},
		{ListBacklog, "Upgrade Postgres to 16", "in_review", "high", []string{"priya"}, day(-1), map[string]any{"component": "infra", "story_points": 5}},
		{ListBacklog, "Remove legacy feature flags", "done", "low", nil, day(-6), map[string]any{"component": "backend", "story_points": 1, "labels": []string{"tech_debt"}}},
		{ListBacklog, "Write ADR for event outbox", "done", "none", []string{"priya"}, nil, map[string]any{"component": "backend"}},
		// Sprint 12: todo(Ready), blocked, in_progress, qa, done  (no in_review here)
		{ListSprint, "Billing page empty state", "todo", "normal", []string{"rahul"}, day(4), map[string]any{"component": "frontend", "story_points": 2, "customer_facing": true}},
		{ListSprint, "Webhook retries with backoff", "todo", "high", []string{"ananya"}, day(6), map[string]any{"component": "backend", "story_points": 5, "target_release": "2026-11-15"}},
		{ListSprint, "SSO for enterprise tenants", "blocked", "urgent", []string{"priya"}, day(2), map[string]any{"component": "backend", "story_points": 13, "customer_facing": true}},
		{ListSprint, "Terraform state lock contention", "blocked", "normal", []string{"ananya"}, nil, map[string]any{"component": "infra", "story_points": 3}},
		{ListSprint, "Kanban drag performance", "in_progress", "high", []string{"rahul", "ananya"}, day(1), map[string]any{"component": "frontend", "story_points": 5}},
		{ListSprint, "Search indexer worker", "in_progress", "normal", []string{"priya"}, day(7), map[string]any{"component": "backend", "story_points": 8}},
		{ListSprint, "Accessibility audit fixes", "qa", "normal", []string{"ananya"}, day(0), map[string]any{"component": "frontend", "story_points": 3, "customer_facing": true}},
		{ListSprint, "Nightly backup verification", "qa", "low", []string{"rahul"}, day(3), map[string]any{"component": "infra", "story_points": 2}},
		{ListSprint, "Release notes generator", "done", "none", []string{"priya"}, day(-2), map[string]any{"component": "backend", "story_points": 2}},
		{ListSprint, "Fix timezone bug in due dates", "done", "urgent", []string{"rahul"}, day(-3), map[string]any{"component": "frontend", "story_points": 1}},
		// Security Audit (private; Ananya + admins only)
		{ListSecurity, "Rotate production DB credentials", "todo", "urgent", []string{"ananya"}, day(1), map[string]any{"component": "infra", "story_points": 3}},
		{ListSecurity, "Pen-test findings triage", "in_progress", "high", []string{"ananya", "priya"}, day(4), map[string]any{"component": "backend"}},
		{ListSecurity, "Enable MFA for admin console", "in_review", "high", []string{"priya"}, day(2), map[string]any{"component": "frontend", "customer_facing": true}},
		// Content Calendar (Marketing; Rahul + admins only)
		{ListContent, "Launch blog post", "todo", "high", []string{"rahul"}, day(8), map[string]any{"channel": "blog", "budget": 0, "brief": "Announce Flowboard 1.0"}},
		{ListContent, "Twitter thread: kanban tips", "scheduled", "normal", []string{"rahul"}, day(3), map[string]any{"channel": "social", "budget": 150}},
		{ListContent, "Customer newsletter (Nov)", "in_progress", "normal", []string{"priya"}, day(12), map[string]any{"channel": "email", "budget": 400}},
		{ListContent, "Case study: Acme Robotics", "done", "low", []string{"rahul"}, day(-4), map[string]any{"channel": "blog", "budget": 1200}},
	}
	colPos := map[string]float64{}
	for i, t := range tasks {
		key := t.list + "/" + t.status
		colPos[key] += 1024
		cf, _ := json.Marshal(t.cf)
		assignees := t.assignees
		if assignees == nil {
			assignees = []string{}
		}
		created := time.Now().UTC().Add(-time.Duration(len(tasks)-i) * time.Hour)
		e.do(`INSERT INTO tasks (id, workspace_id, list_id, title, description, status_key, priority, assignee_ids, due_date, custom_fields, position, created_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'priya',$12,$12)`,
			fmt.Sprintf("tsk_seed_%02d", i+1), WorkspaceID, t.list, t.title, "Seeded task for the Flowboard demo.",
			t.status, t.prio, pq.Array(assignees), t.due, cf, colPos[key], created)
	}
	if e.err == nil {
		slog.Info("seed: created demo workspace", "tasks", len(tasks))
	}
	return e.err
}
