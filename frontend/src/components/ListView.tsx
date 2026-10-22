import { useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { FieldDef, Status, Task, User } from "../api/types";
import { formatFieldValue, initials, isOverdue, priorityLabel, shortDate, userName } from "./util";

interface Props {
  listId: string;
  statuses: Status[];
  fields: FieldDef[];
  users: User[];
  refreshKey: number;
  onOpenTask: (id: string) => void;
  onError: (msg: string) => void;
}

type SortKey = "position" | "dueDate" | "priority" | "title";

export function ListView({ listId, statuses, fields, users, refreshKey, onOpenTask, onError }: Props) {
  const [sort, setSort] = useState<{ key: SortKey; order: "asc" | "desc" }>({ key: "dueDate", order: "asc" });
  const [filterKey, setFilterKey] = useState("");
  const [filterValue, setFilterValue] = useState("");
  const [rows, setRows] = useState<Task[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const filterable = fields.filter((f) => f.type === "single_select" || f.type === "multi_select" || f.type === "checkbox");
  const filterDef = fields.find((f) => f.key === filterKey);
  const fieldColumns = fields.slice(0, 4);

  const params = (c?: string | null): Record<string, string> => {
    const p: Record<string, string> = { sort: sort.key, order: sort.order, limit: "50" };
    if (c) p.cursor = c;
    if (filterKey && filterValue) p[`cf.${filterKey}`] = filterValue;
    return p;
  };

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    api.tasks(listId, params())
      .then((page) => { if (!cancelled) { setRows(page.data); setCursor(page.page.nextCursor); } })
      .catch((e) => onError(e instanceof ApiError ? e.message : "Could not load tasks."))
      .finally(() => !cancelled && setLoading(false));
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [listId, sort, filterKey, filterValue, refreshKey]);

  const loadMore = async () => {
    if (!cursor) return;
    const page = await api.tasks(listId, params(cursor));
    setRows((r) => [...r, ...page.data]);
    setCursor(page.page.nextCursor);
  };

  const toggleSort = (key: SortKey) =>
    setSort((s) => ({ key, order: s.key === key && s.order === "asc" ? "desc" : "asc" }));
  const sortMark = (key: SortKey) => (sort.key === key ? (sort.order === "asc" ? " ↑" : " ↓") : "");
  const ariaSort = (key: SortKey) => (sort.key === key ? (sort.order === "asc" ? "ascending" : "descending") : "none");

  return (
    <div className="listview">
      <div className="toolbar">
        <label>
          Filter by
          <select value={filterKey} onChange={(e) => { setFilterKey(e.target.value); setFilterValue(""); }}>
            <option value="">No filter</option>
            {filterable.map((f) => <option key={f.key} value={f.key}>{f.name}</option>)}
          </select>
        </label>
        {filterDef && (
          <select aria-label={`${filterDef.name} value`} value={filterValue} onChange={(e) => setFilterValue(e.target.value)}>
            <option value="">Any</option>
            {filterDef.type === "checkbox"
              ? [<option key="t" value="true">Checked</option>, <option key="f" value="false">Unchecked</option>]
              : filterDef.options.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
          </select>
        )}
        <span className="toolbar-note">{loading ? "Loading…" : `${rows.length}${cursor ? "+" : ""} tasks`}</span>
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th aria-sort={ariaSort("title")}><button onClick={() => toggleSort("title")}>Title{sortMark("title")}</button></th>
              <th>Status</th>
              <th>Assignees</th>
              <th aria-sort={ariaSort("priority")}><button onClick={() => toggleSort("priority")}>Priority{sortMark("priority")}</button></th>
              <th aria-sort={ariaSort("dueDate")}><button onClick={() => toggleSort("dueDate")}>Due{sortMark("dueDate")}</button></th>
              {fieldColumns.map((f) => <th key={f.key}>{f.name}</th>)}
            </tr>
          </thead>
          <tbody>
            {rows.map((t) => {
              const st = statuses.find((s) => s.key === t.statusKey);
              return (
                <tr key={t.id} onClick={() => onOpenTask(t.id)} tabIndex={0} onKeyDown={(e) => e.key === "Enter" && onOpenTask(t.id)}>
                  <td className="title-cell">{t.title}</td>
                  <td><span className="status-pill" style={{ ["--status" as string]: st?.color }}>{st?.name ?? t.statusKey}</span></td>
                  <td>
                    <span className="avatars">
                      {t.assigneeIds.map((id) => <span key={id} className="avatar" title={userName(users, id)}>{initials(userName(users, id))}</span>)}
                    </span>
                  </td>
                  <td><span className={`prio prio-${t.priority}`}>{priorityLabel[t.priority]}</span></td>
                  <td className={isOverdue(t.dueDate) ? "overdue" : ""}>{shortDate(t.dueDate)}</td>
                  {fieldColumns.map((f) => <td key={f.key}>{formatFieldValue(f, t.customFields[f.key])}</td>)}
                </tr>
              );
            })}
          </tbody>
        </table>
        {!loading && rows.length === 0 && <p className="empty-note">No tasks match. Clear the filter or add a task from the board.</p>}
      </div>
      {cursor && <button className="secondary load-more" onClick={loadMore}>Load more</button>}
    </div>
  );
}
