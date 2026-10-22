package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/flowboard/backend/internal/access"
	"github.com/flowboard/backend/internal/fields"
	"github.com/flowboard/backend/internal/platform/httpx"
	"github.com/flowboard/backend/internal/statuses"
)

type sortSpec struct {
	asc, desc string // SQL expression per direction (NULLs coalesced so keyset works)
	cast      string
}

var sorts = map[string]sortSpec{
	"position":  {"t.position", "t.position", "float8"},
	"dueDate":   {"COALESCE(t.due_date, 'infinity'::timestamptz)", "COALESCE(t.due_date, '-infinity'::timestamptz)", "timestamptz"},
	"priority":  {"t.priority_rank", "t.priority_rank", "int"},
	"createdAt": {"t.created_at", "t.created_at", "timestamptz"},
	"updatedAt": {"t.updated_at", "t.updated_at", "timestamptz"},
	"title":     {"lower(t.title)", "lower(t.title)", "text"},
}

// ListQuery: GET /v1/lists/{id}/tasks
//
//	?limit=1..200 (50) &cursor=... &sort=position|dueDate|priority|createdAt|updatedAt|title
//	&order=asc|desc &statusKey=a,b &assigneeId=rahul &q=text &cf.<fieldKey>=<value>
//
// Pagination is keyset on (sortExpr, id); the cursor encodes the sort it was issued for.
// Due dates sort with nulls last in both directions; priority asc = urgent first.
func (s *Service) List(ctx context.Context, ev *access.Evaluator, listID string, qs url.Values, limit int) (*httpx.Page[*Task], error) {
	cfg, err := loadConfig(ctx, s.DB, ev, listID)
	if err != nil {
		return nil, err
	}
	sortKey := qs.Get("sort")
	if sortKey == "" {
		sortKey = "position"
	}
	spec, ok := sorts[sortKey]
	if !ok {
		return nil, httpx.InvalidField("sort", "INVALID_ENUM", "sort must be one of position, dueDate, priority, createdAt, updatedAt, title")
	}
	order := qs.Get("order")
	if order == "" {
		order = "asc"
	}
	if order != "asc" && order != "desc" {
		return nil, httpx.InvalidField("order", "INVALID_ENUM", "order must be asc or desc")
	}
	expr, cmp, dir := spec.asc, ">", "ASC"
	if order == "desc" {
		expr, cmp, dir = spec.desc, "<", "DESC"
	}
	sig := sortKey + ":" + order
	cur, err := httpx.DecodeCursor(qs.Get("cursor"), sig)
	if err != nil {
		return nil, err
	}

	args := []any{listID}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	where := []string{"t.list_id = $1", "t.deleted_at IS NULL"}

	if v := qs.Get("statusKey"); v != "" {
		where = append(where, "t.status_key = ANY("+arg(pq.Array(strings.Split(v, ",")))+")")
	}
	if v := qs.Get("assigneeId"); v != "" {
		where = append(where, arg(v)+" = ANY(t.assignee_ids)")
	}
	if v := strings.TrimSpace(qs.Get("q")); v != "" {
		where = append(where, "t.title ILIKE "+arg("%"+escapeLike(v)+"%"))
	}
	cfClauses, err := customFieldFilters(cfg.Schema, qs, arg)
	if err != nil {
		return nil, err
	}
	where = append(where, cfClauses...)
	if cur != nil {
		where = append(where, fmt.Sprintf("(%s, t.id) %s (%s::%s, %s)", expr, cmp, arg(cur.Value), spec.cast, arg(cur.ID)))
	}

	query := fmt.Sprintf(`SELECT %s, (%s)::text FROM tasks t WHERE %s ORDER BY %s %s, t.id %s LIMIT %d`,
		taskCols, expr, strings.Join(where, " AND "), expr, dir, dir, limit+1)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &httpx.Page[*Task]{Data: []*Task{}, Page: httpx.PageInfo{Limit: limit}}
	var lastVal string
	for rows.Next() {
		var sv string
		t, err := scanTask(rows, &sv)
		if err != nil {
			return nil, err
		}
		if len(page.Data) == limit {
			last := page.Data[len(page.Data)-1]
			c := httpx.EncodeCursor(httpx.Cursor{Value: lastVal, ID: last.ID, Sort: sig})
			page.Page.NextCursor = &c
			break
		}
		lastVal = sv
		page.Data = append(page.Data, cfg.present(t))
	}
	return page, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// customFieldFilters turns cf.<key>=<value> params into GIN-indexable containment
// predicates (custom_fields @> '{"key": value}'); text fields use ILIKE contains.
func customFieldFilters(schema []fields.Def, qs url.Values, arg func(any) string) ([]string, error) {
	var out []string
	idx := map[string]*fields.Def{}
	for i := range schema {
		idx[schema[i].Key] = &schema[i]
	}
	for param, vals := range qs {
		key, ok := strings.CutPrefix(param, "cf.")
		if !ok || len(vals) == 0 {
			continue
		}
		d := idx[key]
		path := "cf." + key
		if d == nil {
			return nil, httpx.InvalidField(path, "UNKNOWN_FIELD", "no custom field with this key applies to the list")
		}
		v := vals[0]
		var val any
		switch d.Type {
		case "text":
			out = append(out, fmt.Sprintf("t.custom_fields->>%s ILIKE %s", arg(key), arg("%"+escapeLike(v)+"%")))
			continue
		case "number":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, httpx.InvalidField(path, "INVALID_TYPE", "expected a number")
			}
			val = f
		case "checkbox":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, httpx.InvalidField(path, "INVALID_TYPE", "expected true or false")
			}
			if !b { // unchecked = false or absent
				out = append(out, fmt.Sprintf("NOT (t.custom_fields @> %s::jsonb)", arg(mustJSON(map[string]any{key: true}))))
				continue
			}
			val = true
		case "date":
			tm, err := time.Parse("2006-01-02", v)
			if err != nil {
				return nil, httpx.InvalidField(path, "INVALID_FORMAT", "expected YYYY-MM-DD")
			}
			val = tm.Format("2006-01-02")
		case "single_select":
			val = v
		case "multi_select":
			val = []string{v}
		}
		out = append(out, fmt.Sprintf("t.custom_fields @> %s::jsonb", arg(mustJSON(map[string]any{key: val}))))
	}
	return out, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- Board aggregate (the "load board" read) ----

type Column struct {
	Status statuses.Status `json:"status"`
	Tasks  []*Task         `json:"tasks"`
	Count  int             `json:"count"`
}

type Board struct {
	ListID   string            `json:"listId"`
	Statuses []statuses.Status `json:"statuses"`
	Fields   []fields.Def      `json:"fields"`
	Columns  []Column          `json:"columns"`
	// Unmapped counts tasks whose status is not in the resolved set. The config
	// services keep this at 0; it is surfaced so clients and tests can assert it.
	Unmapped  int  `json:"unmapped"`
	Truncated bool `json:"truncated"`
}

const boardCap = 1000

func (s *Service) Board(ctx context.Context, ev *access.Evaluator, listID string, qs url.Values) (*Board, error) {
	cfg, err := loadConfig(ctx, s.DB, ev, listID)
	if err != nil {
		return nil, err
	}
	args := []any{listID}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	where := []string{"t.list_id = $1", "t.deleted_at IS NULL"}
	if v := qs.Get("assigneeId"); v != "" {
		where = append(where, arg(v)+" = ANY(t.assignee_ids)")
	}
	if v := strings.TrimSpace(qs.Get("q")); v != "" {
		where = append(where, "t.title ILIKE "+arg("%"+escapeLike(v)+"%"))
	}
	cf, err := customFieldFilters(cfg.Schema, qs, arg)
	if err != nil {
		return nil, err
	}
	where = append(where, cf...)
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM tasks t WHERE %s ORDER BY t.status_key, t.position, t.id LIMIT %d`,
		taskCols, strings.Join(where, " AND "), boardCap+1), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	b := &Board{ListID: listID, Statuses: cfg.Statuses, Fields: cfg.Schema, Columns: []Column{}}
	byKey := map[string][]*Task{}
	n := 0
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		if n++; n > boardCap {
			b.Truncated = true
			break
		}
		byKey[t.StatusKey] = append(byKey[t.StatusKey], cfg.present(t))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, st := range cfg.Statuses {
		ts := byKey[st.Key]
		if ts == nil {
			ts = []*Task{}
		}
		b.Columns = append(b.Columns, Column{Status: st, Tasks: ts, Count: len(ts)})
		delete(byKey, st.Key)
	}
	for _, ts := range byKey {
		b.Unmapped += len(ts)
	}
	return b, nil
}
