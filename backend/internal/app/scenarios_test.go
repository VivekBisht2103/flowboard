package app_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/flowboard/backend/internal/events"
	"github.com/flowboard/backend/internal/identity"
	"github.com/flowboard/backend/internal/seed"
)

// =========================== Auth ===========================

func TestAuth_MissingCredentialsIs401(t *testing.T) {
	reset(t)
	r := call(t, "", "GET", "/tree", nil)
	expect(t, r, 401)
	if r.errCode() != "UNAUTHENTICATED" {
		t.Fatalf("code = %s", r.errCode())
	}
}

func TestAuth_UnknownUserIs401(t *testing.T) {
	reset(t)
	expect(t, call(t, "mallory", "GET", "/tree", nil), 401)
}

func TestAuth_JWTRoundTripAndRoleClaimIsNotTrusted(t *testing.T) {
	reset(t)
	tok := call(t, "", "POST", "/auth/token", map[string]string{"userId": "bob"})
	expect(t, tok, 200)
	bearer := "Bearer " + tok.Body["token"].(string)
	me := call(t, "", "GET", "/me", nil, "Authorization", bearer)
	expect(t, me, 200)
	if me.Body["id"] != "bob" || me.Body["role"] != "member" {
		t.Fatalf("me = %v", me.Body)
	}

	// A correctly signed token that *claims* admin still resolves Bob's DB role.
	svc := &identity.Service{Cfg: identity.Config{JWTSecret: []byte(jwtSecret)}}
	forged := svc.Sign(identity.Claims{UserID: "bob", Role: "admin", Exp: time.Now().Add(time.Minute).Unix()})
	r := call(t, "", "POST", "/containers/"+seed.ListSprint+"/statuses",
		map[string]any{"key": "wip", "name": "WIP", "category": "active", "color": "#000000"}, "Authorization", "Bearer "+forged)
	expect(t, r, 403)

	// Tampered signature / expired token -> 401.
	expect(t, call(t, "", "GET", "/me", nil, "Authorization", bearer+"x"), 401)
	expired := svc.Sign(identity.Claims{UserID: "bob", Exp: time.Now().Add(-time.Minute).Unix()})
	expect(t, call(t, "", "GET", "/me", nil, "Authorization", "Bearer "+expired), 401)
	// Header and token disagreeing -> 401.
	expect(t, call(t, "carol", "GET", "/me", nil, "Authorization", bearer), 401)
}

// =========================== Permissions ===========================

func treeNames(t *testing.T, user string) []string {
	t.Helper()
	r := call(t, user, "GET", "/tree", nil)
	expect(t, r, 200)
	var names []string
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		names = append(names, n["id"].(string))
		for _, k := range n["children"].([]any) {
			walk(k.(map[string]any))
		}
	}
	walk(r.Body)
	sort.Strings(names)
	return names
}

func TestPermissions_TreeHidesPrivateNodesForBob(t *testing.T) {
	reset(t)
	names := treeNames(t, "bob")
	if slices.Contains(names, seed.ListSecurity) {
		t.Fatal("Bob must not see the private Security Audit list")
	}
	// Bob's allow grant on the private Marketing space reveals it and its children.
	for _, id := range []string{seed.SpaceMkt, seed.FolderCamp, seed.ListContent, seed.ListBacklog, seed.ListSprint} {
		if !slices.Contains(names, id) {
			t.Fatalf("Bob should see %s; got %v", id, names)
		}
	}
}

func TestPermissions_CarolDiffersFromBob(t *testing.T) {
	reset(t)
	names := treeNames(t, "carol")
	if !slices.Contains(names, seed.ListSecurity) {
		t.Fatal("Carol has an allow grant on Security Audit")
	}
	for _, id := range []string{seed.SpaceMkt, seed.ListContent, seed.ListBacklog} {
		if slices.Contains(names, id) {
			t.Fatalf("Carol must not see %s (private without grant / explicit deny)", id)
		}
	}
}

func TestPermissions_AliceSeesAll(t *testing.T) {
	reset(t)
	names := treeNames(t, "alice")
	want := []string{seed.WorkspaceID, seed.SpaceEng, seed.SpaceMkt, seed.FolderQ2, seed.FolderCamp,
		seed.ListBacklog, seed.ListSprint, seed.ListSecurity, seed.ListContent}
	sort.Strings(want)
	if !slices.Equal(names, want) {
		t.Fatalf("alice tree = %v", names)
	}
}

func TestPermissions_DeniedListReturns403OnTaskRead(t *testing.T) {
	reset(t)
	// tsk_seed_20 lives in the private Security Audit list.
	r := call(t, "bob", "GET", "/tasks/tsk_seed_20", nil)
	expect(t, r, 403)
	if r.errCode() != "FORBIDDEN" {
		t.Fatalf("code = %s", r.errCode())
	}
	expect(t, call(t, "carol", "GET", "/tasks/tsk_seed_20", nil), 200)
	// Explicit deny on a *public* list: Carol vs Backlog (tsk_seed_01).
	expect(t, call(t, "carol", "GET", "/tasks/tsk_seed_01", nil), 403)
	expect(t, call(t, "carol", "GET", "/lists/"+seed.ListBacklog+"/board", nil), 403)
	expect(t, call(t, "carol", "PATCH", "/tasks/tsk_seed_01", map[string]any{"title": "x"}), 403)
	// Unknown ids are 404, not 403.
	expect(t, call(t, "carol", "GET", "/tasks/tsk_nope", nil), 404)
}

func TestPermissions_MembersCannotChangeConfigButCanEditTasks(t *testing.T) {
	reset(t)
	expect(t, call(t, "bob", "POST", "/containers/"+seed.ListSprint+"/statuses",
		map[string]any{"key": "wip", "name": "WIP", "category": "active", "color": "#000000"}), 403)
	expect(t, call(t, "bob", "POST", "/containers/"+seed.ListSprint+"/fields",
		map[string]any{"key": "x", "name": "X", "type": "text"}), 403)
	expect(t, call(t, "bob", "POST", "/containers", map[string]any{"parentId": seed.FolderQ2, "type": "list", "name": "Mine"}), 403)
	expect(t, call(t, "bob", "PATCH", "/tasks/tsk_seed_10", map[string]any{"title": "Bob was here"}), 200)
}

func TestPermissions_GrantChangesTakeEffectImmediately(t *testing.T) {
	reset(t)
	expect(t, call(t, "alice", "PUT", "/containers/"+seed.ListSecurity+"/grants/bob", map[string]any{"mode": "allow"}), 200)
	if !slices.Contains(treeNames(t, "bob"), seed.ListSecurity) {
		t.Fatal("allow grant should reveal the list")
	}
	// A deny on the parent folder hides the whole branch even with an allow below.
	expect(t, call(t, "alice", "PUT", "/containers/"+seed.FolderQ2+"/grants/bob", map[string]any{"mode": "deny"}), 200)
	names := treeNames(t, "bob")
	if slices.Contains(names, seed.ListSecurity) || slices.Contains(names, seed.ListSprint) {
		t.Fatalf("deny on folder must hide its lists: %v", names)
	}
}

// =========================== Status resolution ===========================

func TestStatusResolution_SiblingListsResolveDifferentColumnSets(t *testing.T) {
	reset(t)
	backlog, b1 := boardColumns(t, "alice", seed.ListBacklog)
	sprint, b2 := boardColumns(t, "alice", seed.ListSprint)
	if !slices.Equal(backlog, []string{"todo", "in_progress", "in_review", "done"}) {
		t.Fatalf("backlog columns = %v", backlog)
	}
	if !slices.Equal(sprint, []string{"todo", "blocked", "in_progress", "qa", "done"}) {
		t.Fatalf("sprint columns = %v", sprint)
	}
	// Same key, different label: Sprint overrides "todo" -> "Ready".
	name := func(b map[string]any, i int) string {
		return b["columns"].([]any)[i].(map[string]any)["status"].(map[string]any)["name"].(string)
	}
	if name(b1, 0) != "To Do" || name(b2, 0) != "Ready" {
		t.Fatalf("labels: %s / %s", name(b1, 0), name(b2, 0))
	}
	if b1["unmapped"].(float64) != 0 || b2["unmapped"].(float64) != 0 {
		t.Fatal("no task may sit outside the resolved set")
	}
}

func TestStatusResolution_FolderOverrideFlowsToAllChildLists(t *testing.T) {
	reset(t)
	r := call(t, "alice", "PATCH", "/containers/"+seed.FolderQ2+"/statuses/done", map[string]any{"name": "Shipped", "color": "#16a34a"})
	expect(t, r, 200)
	for _, l := range []string{seed.ListBacklog, seed.ListSprint, seed.ListSecurity} {
		rs := call(t, "alice", "GET", "/lists/"+l+"/resolved-statuses", nil)
		for _, s := range rs.Body["data"].([]any) {
			st := s.(map[string]any)
			if st["key"] == "done" && st["name"] != "Shipped" {
				t.Fatalf("%s: done not overridden: %v", l, st)
			}
		}
	}
	// Marketing is a different branch and keeps its own label.
	rs := call(t, "alice", "GET", "/lists/"+seed.ListContent+"/resolved-statuses", nil)
	for _, s := range rs.Body["data"].([]any) {
		if s.(map[string]any)["key"] == "done" && s.(map[string]any)["name"] != "Published" {
			t.Fatal("marketing branch must be unaffected")
		}
	}
}

// =========================== Status validation ===========================

func TestTask_CreateWithUnknownStatusIs400(t *testing.T) {
	reset(t)
	// "qa" exists only in Sprint 12, so it is unknown for Backlog.
	r := call(t, "alice", "POST", "/lists/"+seed.ListBacklog+"/tasks",
		map[string]any{"title": "x", "statusKey": "qa", "customFields": map[string]any{"component": "backend"}})
	expect(t, r, 400)
	if !r.hasFieldError("statusKey", "UNKNOWN_STATUS") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
}

func TestTask_UpdateWithUnknownStatusIs400(t *testing.T) {
	reset(t)
	// in_review is hidden in Sprint 12.
	r := call(t, "alice", "PATCH", "/tasks/tsk_seed_10", map[string]any{"statusKey": "in_review"})
	expect(t, r, 400)
	if !r.hasFieldError("statusKey", "UNKNOWN_STATUS") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
	if getTask(t, "alice", "tsk_seed_10")["statusKey"] != "todo" {
		t.Fatal("task must be unchanged")
	}
}

func TestTask_StatusChangeAppendsToColumnAndBumpsVersion(t *testing.T) {
	reset(t)
	r := call(t, "bob", "PATCH", "/tasks/tsk_seed_10", map[string]any{"statusKey": "qa", "version": 1})
	expect(t, r, 200)
	if r.Body["statusKey"] != "qa" || r.Body["version"].(float64) != 2 {
		t.Fatalf("body = %v", r.Body)
	}
	// Stale version -> 409.
	r = call(t, "bob", "PATCH", "/tasks/tsk_seed_10", map[string]any{"title": "y", "version": 1})
	expect(t, r, 409)
	if r.errCode() != "VERSION_CONFLICT" {
		t.Fatal(r.errCode())
	}
}

// =========================== Cross-list move ===========================

func TestMove_StatusMissingInTarget_MapToDefaultIsCategoryPreserving(t *testing.T) {
	reset(t)
	// tsk_seed_06 is "in_review" (active) in Backlog. Sprint 12 hides in_review.
	r := call(t, "alice", "POST", "/tasks/tsk_seed_06/move", map[string]any{"listId": seed.ListSprint})
	expect(t, r, 200)
	task := r.Body["task"].(map[string]any)
	eff := r.Body["effects"].(map[string]any)
	// First active status in Sprint 12's order is "blocked" (position 1500).
	if task["statusKey"] != "blocked" || task["listId"] != seed.ListSprint {
		t.Fatalf("task = %v", task)
	}
	sm := eff["statusMapped"].(map[string]any)
	if sm["from"] != "in_review" || sm["to"] != "blocked" {
		t.Fatalf("statusMapped = %v", sm)
	}
	_, board := boardColumns(t, "alice", seed.ListSprint)
	if board["unmapped"].(float64) != 0 {
		t.Fatal("board must have no orphaned tasks")
	}
}

func TestMove_StatusMissingInTarget_RejectPolicyIs409AndNoChange(t *testing.T) {
	reset(t)
	r := call(t, "alice", "POST", "/tasks/tsk_seed_06/move", map[string]any{"listId": seed.ListSprint, "statusPolicy": "reject"})
	expect(t, r, 409)
	if r.errCode() != "STATUS_NOT_IN_TARGET" {
		t.Fatal(r.errCode())
	}
	task := getTask(t, "alice", "tsk_seed_06")
	if task["listId"] != seed.ListBacklog || task["statusKey"] != "in_review" {
		t.Fatalf("task must be unchanged: %v", task)
	}
}

func TestMove_ExplicitStatusKeyWinsAndIsValidated(t *testing.T) {
	reset(t)
	r := call(t, "alice", "POST", "/tasks/tsk_seed_06/move", map[string]any{"listId": seed.ListSprint, "statusKey": "in_review"})
	expect(t, r, 400) // in_review is not valid in the destination
	r = call(t, "alice", "POST", "/tasks/tsk_seed_06/move", map[string]any{"listId": seed.ListSprint, "statusKey": "qa"})
	expect(t, r, 200)
	if r.Body["task"].(map[string]any)["statusKey"] != "qa" || r.Body["effects"].(map[string]any)["statusMapped"] != nil {
		t.Fatalf("body = %v", r.Body)
	}
}

func TestMove_StatusPresentInTargetIsKept(t *testing.T) {
	reset(t)
	r := call(t, "alice", "POST", "/tasks/tsk_seed_04/move", map[string]any{"listId": seed.ListSprint}) // in_progress
	expect(t, r, 200)
	if r.Body["task"].(map[string]any)["statusKey"] != "in_progress" {
		t.Fatal("status should be kept")
	}
}

func TestMove_StripsFieldValuesNotInDestinationSchema(t *testing.T) {
	reset(t)
	// tsk_seed_04 has list-level "labels" in Backlog; Sprint 12 has no labels field.
	r := call(t, "alice", "POST", "/tasks/tsk_seed_04/move", map[string]any{"listId": seed.ListSprint})
	expect(t, r, 200)
	eff := r.Body["effects"].(map[string]any)
	if !slices.Equal(toStrings(eff["strippedFields"]), []string{"labels"}) {
		t.Fatalf("strippedFields = %v", eff["strippedFields"])
	}
	task := r.Body["task"].(map[string]any)
	if _, ok := cf(task)["labels"]; ok {
		t.Fatal("labels must be stripped")
	}
	if cf(task)["component"] != "frontend" {
		t.Fatal("inherited space-level values must be kept")
	}
	// Moving back does not resurrect stripped values (strip policy, not hide).
	r = call(t, "alice", "POST", "/tasks/tsk_seed_04/move", map[string]any{"listId": seed.ListBacklog})
	expect(t, r, 200)
	if _, ok := cf(r.Body["task"].(map[string]any))["labels"]; ok {
		t.Fatal("stripped values must not come back")
	}
}

func TestMove_RequiredFieldMissingInDestinationIsRejectedUnlessSupplied(t *testing.T) {
	reset(t)
	// Content Calendar (Marketing) requires "channel"; Engineering tasks don't have it.
	r := call(t, "alice", "POST", "/tasks/tsk_seed_10/move", map[string]any{"listId": seed.ListContent})
	expect(t, r, 400)
	if !r.hasFieldError("customFields.channel", "REQUIRED") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
	if getTask(t, "alice", "tsk_seed_10")["listId"] != seed.ListSprint {
		t.Fatal("rejected move must not change the task")
	}
	r = call(t, "alice", "POST", "/tasks/tsk_seed_10/move", map[string]any{"listId": seed.ListContent, "customFields": map[string]any{"channel": "blog"}})
	expect(t, r, 200)
	task := r.Body["task"].(map[string]any)
	if cf(task)["channel"] != "blog" {
		t.Fatalf("cf = %v", cf(task))
	}
	stripped := toStrings(r.Body["effects"].(map[string]any)["strippedFields"])
	if !slices.Contains(stripped, "component") || !slices.Contains(stripped, "story_points") {
		t.Fatalf("engineering fields should be stripped: %v", stripped)
	}
}

func TestMove_MemberCannotMoveIntoListTheyCannotSee(t *testing.T) {
	reset(t)
	expect(t, call(t, "bob", "POST", "/tasks/tsk_seed_10/move", map[string]any{"listId": seed.ListSecurity}), 403)
}

func TestMove_ReorderWithinColumn(t *testing.T) {
	reset(t)
	// Sprint "todo" column: tsk_seed_10, tsk_seed_11. Move 11 before 10.
	r := call(t, "bob", "POST", "/tasks/tsk_seed_11/move", map[string]any{"beforeTaskId": "tsk_seed_10"})
	expect(t, r, 200)
	_, board := boardColumns(t, "bob", seed.ListSprint)
	col := board["columns"].([]any)[0].(map[string]any)["tasks"].([]any)
	if col[0].(map[string]any)["id"] != "tsk_seed_11" || col[1].(map[string]any)["id"] != "tsk_seed_10" {
		t.Fatal("reorder failed")
	}
	// Drag into another column at a specific spot (after tsk_seed_14 in in_progress).
	r = call(t, "bob", "POST", "/tasks/tsk_seed_11/move", map[string]any{"statusKey": "in_progress", "afterTaskId": "tsk_seed_14"})
	expect(t, r, 200)
	_, board = boardColumns(t, "bob", seed.ListSprint)
	ip := board["columns"].([]any)[2].(map[string]any)["tasks"].([]any)
	var ids []string
	for _, x := range ip {
		ids = append(ids, x.(map[string]any)["id"].(string))
	}
	if !slices.Equal(ids, []string{"tsk_seed_14", "tsk_seed_11", "tsk_seed_15"}) {
		t.Fatalf("in_progress order = %v", ids)
	}
	// Anchor from another column is rejected.
	expect(t, call(t, "bob", "POST", "/tasks/tsk_seed_11/move", map[string]any{"beforeTaskId": "tsk_seed_10"}), 400)
}

func TestMove_RepeatedMidpointInsertsRenumberColumn(t *testing.T) {
	reset(t)
	// Build a column [10, 11, 12], then keep dropping 11 and 12 alternately right
	// after 10. Each drop halves the gap; after ~30 drops the float gap is exhausted
	// and the column must be renumbered transparently.
	expect(t, call(t, "bob", "POST", "/tasks/tsk_seed_12/move", map[string]any{"statusKey": "todo", "afterTaskId": "tsk_seed_11"}), 200)
	last := ""
	for i := 0; i < 80; i++ {
		last = "tsk_seed_11"
		if i%2 == 1 {
			last = "tsk_seed_12"
		}
		expect(t, call(t, "bob", "POST", "/tasks/"+last+"/move", map[string]any{"afterTaskId": "tsk_seed_10"}), 200)
	}
	rows, err := testDB.Query(`SELECT id, position FROM tasks WHERE list_id = $1 AND status_key = 'todo' AND deleted_at IS NULL ORDER BY position`, seed.ListSprint)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	var prev float64 = -1e18
	for rows.Next() {
		var id string
		var pos float64
		_ = rows.Scan(&id, &pos)
		if pos-prev < 1e-6 {
			t.Fatalf("positions collapsed: %v after %v", pos, prev)
		}
		prev = pos
		ids = append(ids, id)
	}
	other := "tsk_seed_12"
	if last == "tsk_seed_12" {
		other = "tsk_seed_11"
	}
	if !slices.Equal(ids, []string{"tsk_seed_10", last, other}) {
		t.Fatalf("order = %v", ids)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// =========================== Custom fields ===========================

func TestCustomFields_WrongTypeIs400WithFieldPath(t *testing.T) {
	reset(t)
	r := call(t, "alice", "POST", "/lists/"+seed.ListBacklog+"/tasks",
		map[string]any{"title": "x", "customFields": map[string]any{"component": "backend", "story_points": "five"}})
	expect(t, r, 400)
	if !r.hasFieldError("customFields.story_points", "INVALID_TYPE") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
	r = call(t, "alice", "PATCH", "/tasks/tsk_seed_01", map[string]any{"customFields": map[string]any{"customer_facing": "yes"}})
	expect(t, r, 400)
	if !r.hasFieldError("customFields.customer_facing", "INVALID_TYPE") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
}

func TestCustomFields_SelectValueNotInOptionsIs400(t *testing.T) {
	reset(t)
	r := call(t, "alice", "POST", "/lists/"+seed.ListBacklog+"/tasks",
		map[string]any{"title": "x", "customFields": map[string]any{"component": "mobile"}})
	expect(t, r, 400)
	if !r.hasFieldError("customFields.component", "INVALID_OPTION") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
	r = call(t, "alice", "PATCH", "/tasks/tsk_seed_01", map[string]any{"customFields": map[string]any{"labels": []string{"bug", "urgent"}}})
	expect(t, r, 400)
	if !r.hasFieldError("customFields.labels[1]", "INVALID_OPTION") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
}

func TestCustomFields_UnknownKeyIs400(t *testing.T) {
	reset(t)
	// "labels" is defined on Backlog only; it does not apply to Sprint 12.
	r := call(t, "alice", "PATCH", "/tasks/tsk_seed_10", map[string]any{"customFields": map[string]any{"labels": []string{"bug"}}})
	expect(t, r, 400)
	if !r.hasFieldError("customFields.labels", "UNKNOWN_FIELD") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
}

func TestCustomFields_ValidPayloadIsAcceptedAndNormalized(t *testing.T) {
	reset(t)
	r := call(t, "bob", "POST", "/lists/"+seed.ListBacklog+"/tasks", map[string]any{
		"title": "Valid task", "priority": "high", "assigneeIds": []string{"bob", "bob", "carol"}, "dueDate": "2026-12-01",
		"customFields": map[string]any{
			"component": "infra", "story_points": 2.5, "target_release": "2026-12-24T23:30:00-05:00",
			"customer_facing": false, "labels": []string{"bug", "chore", "bug"},
		},
	})
	expect(t, r, 201)
	got := cf(r.Body)
	if got["target_release"] != "2026-12-24" {
		t.Fatalf("date not normalised: %v", got["target_release"])
	}
	if !slices.Equal(toStrings(got["labels"]), []string{"bug", "chore"}) {
		t.Fatalf("multi-select not deduplicated: %v", got["labels"])
	}
	if !slices.Equal(toStrings(r.Body["assigneeIds"]), []string{"bob", "carol"}) {
		t.Fatalf("assignees: %v", r.Body["assigneeIds"])
	}
	if r.Body["statusKey"] != "todo" {
		t.Fatal("default status should be the first not_started")
	}
	// Clearing an optional field with null removes it.
	r = call(t, "bob", "PATCH", "/tasks/"+r.Body["id"].(string), map[string]any{"customFields": map[string]any{"labels": nil}})
	expect(t, r, 200)
	if _, ok := cf(r.Body)["labels"]; ok {
		t.Fatal("null should clear")
	}
}

func TestRequiredFields_CreateWithoutRequiredFieldIs400(t *testing.T) {
	reset(t)
	r := call(t, "alice", "POST", "/lists/"+seed.ListSprint+"/tasks", map[string]any{"title": "No component"})
	expect(t, r, 400)
	if !r.hasFieldError("customFields.component", "REQUIRED") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
}

func TestRequiredFields_UpdateCannotClearRequiredField(t *testing.T) {
	reset(t)
	r := call(t, "alice", "PATCH", "/tasks/tsk_seed_10", map[string]any{"customFields": map[string]any{"component": nil}})
	expect(t, r, 400)
	if !r.hasFieldError("customFields.component", "REQUIRED") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
}

func TestRequiredFields_MakingFieldRequiredNeedsBackfill(t *testing.T) {
	reset(t)
	// story_points is optional and missing on several tasks.
	r := call(t, "alice", "PATCH", "/containers/"+seed.SpaceEng+"/fields/fld_eng_points", map[string]any{"required": true})
	expect(t, r, 409)
	if r.errCode() != "FIELD_REQUIRED_BACKFILL" {
		t.Fatal(r.errCode())
	}
	r = call(t, "alice", "PATCH", "/containers/"+seed.SpaceEng+"/fields/fld_eng_points", map[string]any{"required": true, "defaultValue": 1})
	expect(t, r, 200)
	if r.Body["effects"].(map[string]any)["backfilledTasks"].(float64) < 1 {
		t.Fatal("expected backfill")
	}
	if cf(getTask(t, "alice", "tsk_seed_09"))["story_points"] != 1.0 {
		t.Fatal("tsk_seed_09 should be backfilled")
	}
}

// =========================== Config safety ===========================

func TestConfigSafety_DeleteInUseStatusIs409AndBoardUnchanged(t *testing.T) {
	reset(t)
	before, _ := boardColumns(t, "alice", seed.ListSprint)
	r := call(t, "alice", "DELETE", "/containers/"+seed.ListSprint+"/statuses/qa", nil)
	expect(t, r, 409)
	if r.errCode() != "STATUS_IN_USE" {
		t.Fatal(r.errCode())
	}
	after, board := boardColumns(t, "alice", seed.ListSprint)
	if !slices.Equal(before, after) || board["unmapped"].(float64) != 0 {
		t.Fatalf("board changed: %v -> %v", before, after)
	}
}

func TestConfigSafety_DeleteInUseStatusWithRemapMovesTasks(t *testing.T) {
	reset(t)
	r := call(t, "alice", "DELETE", "/containers/"+seed.ListSprint+"/statuses/qa?remapTo=in_progress", nil)
	expect(t, r, 200)
	remaps := r.Body["effects"].(map[string]any)["remapped"].([]any)
	if len(remaps) != 1 || remaps[0].(map[string]any)["taskCount"].(float64) != 2 {
		t.Fatalf("remapped = %v", remaps)
	}
	cols, board := boardColumns(t, "alice", seed.ListSprint)
	if slices.Contains(cols, "qa") || board["unmapped"].(float64) != 0 {
		t.Fatalf("cols = %v unmapped = %v", cols, board["unmapped"])
	}
	if getTask(t, "alice", "tsk_seed_16")["statusKey"] != "in_progress" {
		t.Fatal("task should be remapped")
	}
	// A remap target that is not valid after the change is rejected.
	r = call(t, "alice", "DELETE", "/containers/"+seed.ListSprint+"/statuses/blocked?remapTo=qa", nil)
	expect(t, r, 400)
}

func TestConfigSafety_HidingInheritedStatusChecksEveryDescendantList(t *testing.T) {
	reset(t)
	// Hiding in_review at the folder affects Backlog and Security (both have tasks in it).
	r := call(t, "alice", "PATCH", "/containers/"+seed.FolderQ2+"/statuses/in_review", map[string]any{"hidden": true})
	expect(t, r, 409)
	inUse := r.Body["error"].(map[string]any)["details"].(map[string]any)["inUse"].([]any)
	if len(inUse) != 2 {
		t.Fatalf("expected two affected lists, got %v", inUse)
	}
	r = call(t, "alice", "PATCH", "/containers/"+seed.FolderQ2+"/statuses/in_review?remapTo=in_progress", map[string]any{"hidden": true})
	expect(t, r, 200)
	for _, l := range []string{seed.ListBacklog, seed.ListSecurity} {
		cols, board := boardColumns(t, "alice", l)
		if slices.Contains(cols, "in_review") || board["unmapped"].(float64) != 0 {
			t.Fatalf("%s: cols %v unmapped %v", l, cols, board["unmapped"])
		}
	}
}

func TestConfigSafety_CannotRemoveLastDoneStatus(t *testing.T) {
	reset(t)
	r := call(t, "alice", "PATCH", "/containers/"+seed.ListBacklog+"/statuses/done?remapTo=todo", map[string]any{"hidden": true})
	expect(t, r, 422)
	if r.errCode() != "STATUS_SET_INVALID" {
		t.Fatal(r.errCode())
	}
	// Re-categorising the only active status at workspace level breaks lists that have no other active status.
	r = call(t, "alice", "PATCH", "/containers/"+seed.WorkspaceID+"/statuses/in_progress", map[string]any{"category": "done"})
	expect(t, r, 200) // every list still has another active status (in_review / blocked / scheduled)
}

func TestConfigSafety_StatusCRUDAndReorder(t *testing.T) {
	reset(t)
	r := call(t, "alice", "POST", "/containers/"+seed.ListBacklog+"/statuses", map[string]any{"key": "triage", "name": "Triage", "category": "not_started", "color": "#123456", "position": 500})
	expect(t, r, 201)
	r = call(t, "alice", "POST", "/containers/"+seed.ListBacklog+"/statuses", map[string]any{"key": "nope", "name": "Missing category"})
	expect(t, r, 400)
	if !r.hasFieldError("category", "REQUIRED") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
	cols, _ := boardColumns(t, "alice", seed.ListBacklog)
	if cols[0] != "triage" {
		t.Fatalf("cols = %v", cols)
	}
	r = call(t, "alice", "PUT", "/containers/"+seed.ListBacklog+"/statuses/order", map[string]any{"keys": []string{"todo", "triage", "in_review", "in_progress", "done"}})
	expect(t, r, 200)
	cols, _ = boardColumns(t, "alice", seed.ListBacklog)
	if !slices.Equal(cols, []string{"todo", "triage", "in_review", "in_progress", "done"}) {
		t.Fatalf("cols = %v", cols)
	}
	// Sibling list is untouched by Backlog's local config.
	sprint, _ := boardColumns(t, "alice", seed.ListSprint)
	if !slices.Equal(sprint, []string{"todo", "blocked", "in_progress", "qa", "done"}) {
		t.Fatalf("sprint cols = %v", sprint)
	}
	expect(t, call(t, "alice", "PUT", "/containers/"+seed.ListBacklog+"/statuses/order", map[string]any{"keys": []string{"todo"}}), 400)
}

func TestConfigSafety_DeleteFieldStripsValues(t *testing.T) {
	reset(t)
	r := call(t, "alice", "DELETE", "/containers/"+seed.ListBacklog+"/fields/fld_backlog_labels", nil)
	expect(t, r, 200)
	if r.Body["effects"].(map[string]any)["strippedTasks"].(float64) < 5 {
		t.Fatalf("effects = %v", r.Body["effects"])
	}
	var n int
	if err := testDB.QueryRowContext(context.Background(), `SELECT count(*) FROM tasks WHERE custom_fields ? 'labels'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d tasks still carry orphaned label values", n)
	}
	rf := call(t, "alice", "GET", "/lists/"+seed.ListBacklog+"/resolved-fields", nil)
	for _, f := range rf.Body["data"].([]any) {
		if f.(map[string]any)["key"] == "labels" {
			t.Fatal("deleted field still resolves")
		}
	}
}

func TestConfigSafety_RemovingSelectOptionInUseIs409(t *testing.T) {
	reset(t)
	r := call(t, "alice", "PATCH", "/containers/"+seed.SpaceEng+"/fields/fld_eng_component", map[string]any{
		"options": []map[string]any{{"id": "frontend", "label": "Frontend"}, {"id": "backend", "label": "Backend"}}})
	expect(t, r, 409)
	if r.errCode() != "OPTION_IN_USE" {
		t.Fatal(r.errCode())
	}
	// Renaming a label (same id) is fine.
	r = call(t, "alice", "PATCH", "/containers/"+seed.SpaceEng+"/fields/fld_eng_component", map[string]any{
		"options": []map[string]any{{"id": "frontend", "label": "Web"}, {"id": "backend", "label": "API"}, {"id": "infra", "label": "Platform"}}})
	expect(t, r, 200)
}

func TestConfigSafety_FieldKeyCollisionAcrossLevelsIs409AndTypeIsImmutable(t *testing.T) {
	reset(t)
	// component exists on the Engineering space; redefining it below is ambiguous.
	r := call(t, "alice", "POST", "/containers/"+seed.ListSprint+"/fields", map[string]any{"key": "component", "name": "Component", "type": "text"})
	expect(t, r, 409)
	if r.errCode() != "FIELD_KEY_CONFLICT" {
		t.Fatal(r.errCode())
	}
	// labels exists on Backlog (a descendant); defining it on the folder would collide.
	expect(t, call(t, "alice", "POST", "/containers/"+seed.FolderQ2+"/fields", map[string]any{"key": "labels", "name": "Labels", "type": "text"}), 409)
	// The same key in an unrelated branch is fine.
	expect(t, call(t, "alice", "POST", "/containers/"+seed.ListContent+"/fields", map[string]any{"key": "labels", "name": "Labels", "type": "text"}), 201)
	r = call(t, "alice", "PATCH", "/containers/"+seed.SpaceEng+"/fields/fld_eng_points", map[string]any{"type": "text"})
	expect(t, r, 400)
	if !r.hasFieldError("type", "IMMUTABLE") {
		t.Fatalf("errors = %v", r.fieldErrors())
	}
}

// =========================== API contract ===========================

func TestAPI_CursorPaginationSortedByDueDateNullsLast(t *testing.T) {
	reset(t)
	var all []map[string]any
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		path := "/lists/" + seed.ListBacklog + "/tasks?sort=dueDate&limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		r := call(t, "alice", "GET", path, nil)
		expect(t, r, 200)
		for _, x := range r.Body["data"].([]any) {
			all = append(all, x.(map[string]any))
		}
		next, _ := r.Body["page"].(map[string]any)["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(all) != 9 {
		t.Fatalf("expected all 9 backlog tasks, got %d", len(all))
	}
	seen := map[string]bool{}
	sawNull := false
	prev := ""
	for _, x := range all {
		id := x["id"].(string)
		if seen[id] {
			t.Fatal("duplicate across pages: " + id)
		}
		seen[id] = true
		due, _ := x["dueDate"].(string)
		if due == "" {
			sawNull = true
			continue
		}
		if sawNull {
			t.Fatal("null due dates must sort last")
		}
		if due < prev {
			t.Fatal("not sorted")
		}
		prev = due
	}
	// A cursor cannot be replayed against another sort.
	expect(t, call(t, "alice", "GET", "/lists/"+seed.ListBacklog+"/tasks?sort=priority&cursor="+cursor, nil), 400)
}

func TestAPI_PrioritySortAndCustomFieldFilter(t *testing.T) {
	reset(t)
	r := call(t, "alice", "GET", "/lists/"+seed.ListSprint+"/tasks?sort=priority&limit=200", nil)
	expect(t, r, 200)
	data := r.Body["data"].([]any)
	if data[0].(map[string]any)["priority"] != "urgent" || data[len(data)-1].(map[string]any)["priority"] != "none" {
		t.Fatal("priority asc should be urgent..none")
	}
	r = call(t, "alice", "GET", "/lists/"+seed.ListSprint+"/tasks?cf.component=frontend&limit=200", nil)
	expect(t, r, 200)
	for _, x := range r.Body["data"].([]any) {
		if cf(x.(map[string]any))["component"] != "frontend" {
			t.Fatal("filter leaked")
		}
	}
	if len(r.Body["data"].([]any)) != 4 {
		t.Fatalf("expected 4 frontend tasks, got %d", len(r.Body["data"].([]any)))
	}
	expect(t, call(t, "alice", "GET", "/lists/"+seed.ListSprint+"/tasks?cf.labels=bug", nil), 400) // not in this list's schema
	expect(t, call(t, "alice", "GET", "/lists/"+seed.ListSprint+"/tasks?cf.story_points=abc", nil), 400)
}

func TestAPI_ErrorEnvelopeAndUnknownBodyFields(t *testing.T) {
	reset(t)
	r := call(t, "alice", "PATCH", "/tasks/tsk_seed_01", map[string]any{"role": "admin"})
	expect(t, r, 400)
	e := r.Body["error"].(map[string]any)
	if e["code"] == nil || e["message"] == nil {
		t.Fatalf("bad envelope: %s", r.Raw)
	}
	expect(t, call(t, "alice", "GET", "/nope", nil), 404)
}

// =========================== Async path ===========================

func TestOutbox_WorkerBuildsActivityIdempotently(t *testing.T) {
	reset(t)
	expect(t, call(t, "bob", "PATCH", "/tasks/tsk_seed_10", map[string]any{"statusKey": "in_progress"}), 200)
	// Eventually consistent: nothing in the feed until the worker runs.
	r := call(t, "bob", "GET", "/tasks/tsk_seed_10/activity", nil)
	if len(r.Body["data"].([]any)) != 0 {
		t.Fatal("feed should be empty before the worker runs")
	}
	if n := drainOutbox(t); n != 1 {
		t.Fatalf("drained %d", n)
	}
	r = call(t, "bob", "GET", "/tasks/tsk_seed_10/activity", nil)
	feed := r.Body["data"].([]any)
	if len(feed) != 1 || feed[0].(map[string]any)["type"] != "task.status_changed" {
		t.Fatalf("feed = %s", r.Raw)
	}
	// Simulate redelivery (crash after side effect, before ack): no duplicate.
	if _, err := testDB.Exec(`UPDATE outbox SET processed_at = NULL`); err != nil {
		t.Fatal(err)
	}
	drainOutbox(t)
	r = call(t, "bob", "GET", "/tasks/tsk_seed_10/activity", nil)
	if len(r.Body["data"].([]any)) != 1 {
		t.Fatal("redelivery must be idempotent")
	}
	// The activity endpoint enforces the same visibility as the task.
	expect(t, call(t, "bob", "GET", "/tasks/tsk_seed_20/activity", nil), 403)
}

func TestOutbox_RolledBackWriteEmitsNothing(t *testing.T) {
	reset(t)
	expect(t, call(t, "alice", "PATCH", "/tasks/tsk_seed_10", map[string]any{"statusKey": "nope"}), 400)
	var n int
	_ = testDB.QueryRow(`SELECT count(*) FROM outbox`).Scan(&n)
	if n != 0 {
		t.Fatal("failed writes must not leave events behind")
	}
}

// =========================== Hierarchy ===========================

func TestHierarchy_ParentTypeRulesAndReorder(t *testing.T) {
	reset(t)
	expect(t, call(t, "alice", "POST", "/containers", map[string]any{"parentId": seed.SpaceEng, "type": "list", "name": "Bad"}), 400)
	expect(t, call(t, "alice", "POST", "/containers", map[string]any{"parentId": seed.ListSprint, "type": "list", "name": "Bad"}), 400)
	r := call(t, "alice", "POST", "/containers", map[string]any{"parentId": seed.FolderQ2, "type": "list", "name": "Icebox"})
	expect(t, r, 201)
	id := r.Body["id"].(string)
	// New list inherits statuses and fields immediately.
	cols, _ := boardColumns(t, "alice", id)
	if !slices.Equal(cols, []string{"todo", "in_progress", "in_review", "done"}) {
		t.Fatalf("cols = %v", cols)
	}
	expect(t, call(t, "alice", "POST", "/containers/"+id+"/move", map[string]any{"beforeId": seed.ListBacklog}), 200)
	ch := call(t, "alice", "GET", "/containers/"+seed.FolderQ2+"/children", nil)
	if ch.Body["data"].([]any)[0].(map[string]any)["id"] != id {
		t.Fatal("reorder failed")
	}
	// Paginated children.
	p := call(t, "alice", "GET", "/containers/"+seed.FolderQ2+"/children?limit=2", nil)
	next := p.Body["page"].(map[string]any)["nextCursor"].(string)
	p2 := call(t, "alice", "GET", "/containers/"+seed.FolderQ2+"/children?limit=2&cursor="+next, nil)
	if len(p.Body["data"].([]any))+len(p2.Body["data"].([]any)) != 4 {
		t.Fatal("pagination lost items")
	}
	// Archive hides the subtree.
	expect(t, call(t, "alice", "DELETE", "/containers/"+seed.FolderQ2, nil), 204)
	expect(t, call(t, "alice", "GET", "/lists/"+seed.ListSprint+"/board", nil), 404)
}

// =========================== Seed integrity ===========================

func TestSeed_EveryListBoardIsConsistent(t *testing.T) {
	reset(t)
	total := 0
	for _, l := range []string{seed.ListBacklog, seed.ListSprint, seed.ListSecurity, seed.ListContent} {
		_, board := boardColumns(t, "alice", l)
		if board["unmapped"].(float64) != 0 {
			t.Fatalf("%s has unmapped tasks", l)
		}
		for _, c := range board["columns"].([]any) {
			total += int(c.(map[string]any)["count"].(float64))
		}
		// Every seeded task re-validates (types, options, required) against its
		// list's schema: re-submitting its own values must succeed.
		for _, c := range board["columns"].([]any) {
			for _, tk := range c.(map[string]any)["tasks"].([]any) {
				task := tk.(map[string]any)
				expect(t, call(t, "alice", "PATCH", "/tasks/"+task["id"].(string), map[string]any{"customFields": cf(task)}), 200)
			}
		}
	}
	if total < 20 {
		t.Fatalf("seed must have 20+ tasks, has %d", total)
	}
}

type failingConsumer struct{}

func (failingConsumer) Name() string { return "always-fails" }
func (failingConsumer) Handle(context.Context, *sql.Tx, *events.Event, []byte) error {
	return errors.New("downstream unavailable")
}

func TestOutbox_FailuresBackOffThenDeadLetterWithoutPartialWrites(t *testing.T) {
	reset(t)
	expect(t, call(t, "bob", "PATCH", "/tasks/tsk_seed_10", map[string]any{"title": "retry me"}), 200)
	// Activity projector runs first and succeeds, then the second consumer fails:
	// the savepoint must roll back the activity insert too.
	w := &events.Worker{DB: testDB, MaxAttempts: 3, Consumers: []events.Consumer{events.ActivityProjector{}, failingConsumer{}}}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := w.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		_, _ = testDB.Exec(`UPDATE outbox SET available_at = now()`) // skip the backoff wait
	}
	var attempts int
	var dead bool
	var lastErr string
	if err := testDB.QueryRow(`SELECT attempts, dead, last_error FROM outbox`).Scan(&attempts, &dead, &lastErr); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || !dead || lastErr == "" {
		t.Fatalf("attempts=%d dead=%v err=%q", attempts, dead, lastErr)
	}
	var n int
	_ = testDB.QueryRow(`SELECT count(*) FROM activity`).Scan(&n)
	if n != 0 {
		t.Fatal("failed dispatch must not leave partial consumer writes")
	}
}
