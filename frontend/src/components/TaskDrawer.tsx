import { useEffect, useMemo, useState } from "react";
import { api, ApiError, type TaskPatch } from "../api/client";
import type { Activity, FieldDef, FieldValue, Priority, Status, Task, User } from "../api/types";
import { PRIORITIES } from "../api/types";
import { FieldInput } from "./FieldInput";
import { priorityLabel } from "./util";

interface Props {
  listId: string;
  statuses: Status[];
  fields: FieldDef[];
  users: User[];
  lists: { id: string; name: string }[];
  taskId?: string;
  createStatusKey?: string;
  onClose: () => void;
  onSaved: (message: string) => void;
}

interface Draft {
  title: string;
  description: string;
  statusKey: string;
  priority: Priority;
  assigneeIds: string[];
  dueDate: string; // YYYY-MM-DD or ""
  customFields: Record<string, FieldValue | null>;
}

const toDraft = (t: Task): Draft => ({
  title: t.title,
  description: t.description,
  statusKey: t.statusKey,
  priority: t.priority,
  assigneeIds: t.assigneeIds,
  dueDate: t.dueDate ? t.dueDate.slice(0, 10) : "",
  customFields: { ...t.customFields },
});

export function TaskDrawer({ listId, statuses, fields, users, lists, taskId, createStatusKey, onClose, onSaved }: Props) {
  const creating = !taskId;
  const [task, setTask] = useState<Task | null>(null);
  const [draft, setDraft] = useState<Draft | null>(
    creating
      ? { title: "", description: "", statusKey: createStatusKey ?? statuses[0]?.key ?? "", priority: "none", assigneeIds: [], dueDate: "", customFields: {} }
      : null,
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [banner, setBanner] = useState("");
  const [saving, setSaving] = useState(false);
  const [activity, setActivity] = useState<Activity[]>([]);

  useEffect(() => {
    if (!taskId) return;
    api.task(taskId).then((t) => { setTask(t); setDraft(toDraft(t)); }).catch((e) => setBanner(e.message));
    api.taskActivity(taskId).then((p) => setActivity(p.data)).catch(() => undefined);
  }, [taskId]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const patch = useMemo((): TaskPatch => {
    if (!draft) return {};
    if (creating || !task) {
      const cf = Object.fromEntries(Object.entries(draft.customFields).filter(([, v]) => v !== null));
      return {
        title: draft.title, description: draft.description, statusKey: draft.statusKey, priority: draft.priority,
        assigneeIds: draft.assigneeIds, dueDate: draft.dueDate || null, customFields: cf,
      };
    }
    const p: TaskPatch = { version: task.version };
    if (draft.title !== task.title) p.title = draft.title;
    if (draft.description !== task.description) p.description = draft.description;
    if (draft.statusKey !== task.statusKey) p.statusKey = draft.statusKey;
    if (draft.priority !== task.priority) p.priority = draft.priority;
    if (draft.assigneeIds.join() !== task.assigneeIds.join()) p.assigneeIds = draft.assigneeIds;
    const due = draft.dueDate || null;
    if (due !== (task.dueDate ? task.dueDate.slice(0, 10) : null)) p.dueDate = due;
    const cf: Record<string, FieldValue | null> = {};
    for (const f of fields) {
      const before = task.customFields[f.key];
      const after = draft.customFields[f.key];
      if (JSON.stringify(before ?? null) !== JSON.stringify(after ?? null)) cf[f.key] = after ?? null;
    }
    if (Object.keys(cf).length) p.customFields = cf;
    return p;
  }, [draft, task, creating, fields]);

  const dirty = creating || Object.keys(patch).some((k) => k !== "version");

  const showErrors = (e: unknown) => {
    if (e instanceof ApiError) {
      const map: Record<string, string> = {};
      e.fieldErrors.forEach((fe) => { map[fe.path.replace(/\[\d+\]$/, "")] = fe.message; });
      setErrors(map);
      setBanner(e.fieldErrors.length ? "Fix the highlighted fields and save again." : e.message);
    } else {
      setBanner("Could not save. Check your connection and try again.");
    }
  };

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    setErrors({});
    setBanner("");
    try {
      if (creating) {
        await api.createTask(listId, { ...patch, title: draft.title });
        onSaved(`Created “${draft.title}”`);
      } else if (task) {
        const t = await api.updateTask(task.id, patch);
        setTask(t);
        setDraft(toDraft(t));
        onSaved(`Saved “${t.title}”`);
      }
    } catch (e) {
      showErrors(e);
    } finally {
      setSaving(false);
    }
  };

  const moveToList = async (dest: string) => {
    if (!task || !dest) return;
    try {
      const res = await api.moveTask(task.id, { listId: dest, version: task.version });
      const notes = [];
      if (res.effects.statusMapped) notes.push(`status ${res.effects.statusMapped.from} → ${res.effects.statusMapped.to}`);
      if (res.effects.strippedFields.length) notes.push(`removed ${res.effects.strippedFields.join(", ")}`);
      onSaved(`Moved “${task.title}”${notes.length ? ` (${notes.join("; ")})` : ""}`);
      onClose();
    } catch (e) {
      showErrors(e);
      if (e instanceof ApiError && e.fieldErrors.length) {
        setBanner(`${e.message}. ${e.fieldErrors.map((f) => f.message).join("; ")}.`);
      }
    }
  };

  const remove = async () => {
    if (!task || !confirm(`Delete “${task.title}”?`)) return;
    await api.deleteTask(task.id);
    onSaved(`Deleted “${task.title}”`);
    onClose();
  };

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => (d ? { ...d, [k]: v } : d));

  return (
    <div className="drawer-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <aside className="drawer" role="dialog" aria-modal="true" aria-label={creating ? "New task" : "Task details"}>
        <header className="drawer-head">
          <span className="drawer-kind">{creating ? "New task" : task?.id}</span>
          <button className="ghost" onClick={onClose} aria-label="Close">✕</button>
        </header>
        {!draft ? <p className="empty-note">Loading…</p> : (
          <form className="drawer-body" onSubmit={(e) => { e.preventDefault(); save(); }}>
            {banner && <p className="banner" role="alert">{banner}</p>}
            <div className={`field ${errors.title ? "has-error" : ""}`}>
              <label htmlFor="title" className="field-label">Title<span className="req">*</span></label>
              <input id="title" className="title-input" value={draft.title} maxLength={500} autoFocus={creating}
                onChange={(e) => set("title", e.target.value)} />
              {errors.title && <p className="field-error">{errors.title}</p>}
            </div>
            <div className="grid-2">
              <div className={`field ${errors.statusKey ? "has-error" : ""}`}>
                <label htmlFor="status" className="field-label">Status</label>
                <select id="status" value={draft.statusKey} onChange={(e) => set("statusKey", e.target.value)}>
                  {statuses.map((s) => <option key={s.key} value={s.key}>{s.name}</option>)}
                </select>
                {errors.statusKey && <p className="field-error">{errors.statusKey}</p>}
              </div>
              <div className="field">
                <label htmlFor="priority" className="field-label">Priority</label>
                <select id="priority" value={draft.priority} onChange={(e) => set("priority", e.target.value as Priority)}>
                  {PRIORITIES.map((p) => <option key={p} value={p}>{priorityLabel[p]}</option>)}
                </select>
              </div>
              <div className="field">
                <label htmlFor="due" className="field-label">Due date</label>
                <input id="due" type="date" value={draft.dueDate} onChange={(e) => set("dueDate", e.target.value)} />
              </div>
              <div className="field">
                <span className="field-label">Assignees</span>
                <div className="chips" role="group" aria-label="Assignees">
                  {users.map((u) => {
                    const on = draft.assigneeIds.includes(u.id);
                    return (
                      <button type="button" key={u.id} className={`chip ${on ? "on" : ""}`} aria-pressed={on}
                        onClick={() => set("assigneeIds", on ? draft.assigneeIds.filter((a) => a !== u.id) : [...draft.assigneeIds, u.id])}>
                        {u.name.split(" ")[0]}
                      </button>
                    );
                  })}
                </div>
              </div>
            </div>
            <div className="field">
              <label htmlFor="desc" className="field-label">Description</label>
              <textarea id="desc" rows={4} value={draft.description} onChange={(e) => set("description", e.target.value)} />
            </div>

            {fields.length > 0 && (
              <fieldset className="custom-fields">
                <legend>Custom fields</legend>
                {fields.map((f) => (
                  <FieldInput key={f.key} def={f} value={draft.customFields[f.key] ?? undefined}
                    error={errors[`customFields.${f.key}`]}
                    onChange={(v) => set("customFields", { ...draft.customFields, [f.key]: v })} />
                ))}
              </fieldset>
            )}

            <div className="drawer-actions">
              <button type="submit" className="primary" disabled={saving || !dirty}>
                {saving ? "Saving…" : creating ? "Create task" : "Save changes"}
              </button>
              {!creating && (
                <>
                  <label className="move-to">
                    Move to
                    <select value="" onChange={(e) => moveToList(e.target.value)}>
                      <option value="">another list…</option>
                      {lists.filter((l) => l.id !== listId).map((l) => <option key={l.id} value={l.id}>{l.name}</option>)}
                    </select>
                  </label>
                  <button type="button" className="danger ghost" onClick={remove}>Delete</button>
                </>
              )}
            </div>

            {!creating && (
              <section className="activity" aria-label="Activity">
                <h4>Activity</h4>
                {activity.length === 0
                  ? <p className="empty-note">No activity yet. Changes appear here once the worker processes them.</p>
                  : <ol>{activity.map((a) => <li key={a.id}><span>{a.summary}</span><time>{new Date(a.createdAt).toLocaleString()}</time></li>)}</ol>}
              </section>
            )}
          </form>
        )}
      </aside>
    </div>
  );
}
