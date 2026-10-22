import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { FieldType, FieldView, StatusCategory, StatusView, TreeNode } from "../api/types";

interface Props {
  node: TreeNode;
  isAdmin: boolean;
  onChanged: (msg: string) => void;
}

const CATEGORIES: StatusCategory[] = ["not_started", "active", "done", "closed"];
const catLabel: Record<StatusCategory, string> = { not_started: "Not started", active: "Active", done: "Done", closed: "Closed" };
const FIELD_TYPES: FieldType[] = ["text", "number", "single_select", "multi_select", "date", "checkbox"];
const typeLabel: Record<FieldType, string> = {
  text: "Text", number: "Number", single_select: "Single select", multi_select: "Multi select", date: "Date", checkbox: "Checkbox",
};

const slug = (s: string) => s.toLowerCase().trim().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "");

interface Pending {
  message: string;
  run: (remapTo: string) => Promise<unknown>;
}

export function ConfigPanel({ node, isAdmin, onChanged }: Props) {
  const [sv, setSv] = useState<StatusView | null>(null);
  const [fv, setFv] = useState<FieldView | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState<Pending | null>(null);
  const [remapTo, setRemapTo] = useState("");

  const load = useCallback(async () => {
    const [s, f] = await Promise.all([api.statusView(node.id), api.fieldView(node.id)]);
    setSv(s);
    setFv(f);
  }, [node.id]);

  useEffect(() => { setError(""); setPending(null); load().catch((e) => setError(e.message)); }, [load]);

  /** Runs a mutation; a STATUS_IN_USE conflict turns into a remap prompt. */
  const run = async (label: string, fn: (remap?: string) => Promise<unknown>) => {
    setError("");
    try {
      await fn();
      await load();
      onChanged(label);
    } catch (e) {
      if (e instanceof ApiError && e.code === "STATUS_IN_USE") {
        const inUse = (e.details?.inUse as { listName: string; taskCount: number; statusKey: string }[]) ?? [];
        setPending({
          message: inUse.map((u) => `${u.taskCount} task(s) in ${u.listName} use “${u.statusKey}”`).join(", "),
          run: async (r) => { await fn(r); await load(); onChanged(label); },
        });
        setRemapTo("");
      } else {
        setError(e instanceof ApiError ? e.message : String(e));
      }
    }
  };

  if (!sv || !fv) return <div className="config">{error ? <p className="banner">{error}</p> : <p className="empty-note">Loading…</p>}</div>;

  const localByKey = new Map(sv.local.map((d) => [d.key, d]));
  const hiddenHere = sv.local.filter((d) => d.hidden);
  const scopeName = node.type === "workspace" ? "the workspace" : `this ${node.type}`;
  const readOnly = !isAdmin;

  const move = (key: string, dir: -1 | 1) => {
    const keys = sv.resolved.map((s) => s.key);
    const i = keys.indexOf(key);
    const j = i + dir;
    if (j < 0 || j >= keys.length) return;
    [keys[i], keys[j]] = [keys[j], keys[i]];
    run("Reordered statuses", () => api.reorderStatuses(node.id, keys));
  };

  return (
    <div className="config">
      {readOnly && <p className="banner subtle">Only admins can change statuses and fields. You are viewing what {scopeName} resolves to.</p>}
      {error && <p className="banner" role="alert">{error}</p>}
      {pending && (
        <div className="banner remap" role="alert">
          <p>{pending.message}. Choose where those tasks should go:</p>
          <select aria-label="Move tasks to" value={remapTo} onChange={(e) => setRemapTo(e.target.value)}>
            <option value="">Pick a status…</option>
            {sv.resolved.map((s) => <option key={s.key} value={s.key}>{s.name}</option>)}
          </select>
          <button className="primary" disabled={!remapTo}
            onClick={() => pending.run(remapTo).then(() => setPending(null)).catch((e) => setError(e.message))}>Move tasks and apply</button>
          <button className="ghost" onClick={() => setPending(null)}>Cancel</button>
        </div>
      )}

      <section className="config-section">
        <header>
          <h2>Statuses</h2>
          <p>Columns on every board under {scopeName}. Changes here override what is inherited from above and apply to everything below.</p>
        </header>
        <table className="config-table">
          <thead><tr><th>Order</th><th>Name</th><th>Category</th><th>Color</th><th>Comes from</th><th /></tr></thead>
          <tbody>
            {sv.resolved.map((s, i) => {
              const local = localByKey.get(s.key);
              const definedHere = s.ownerId === node.id;
              const overriddenHere = !definedHere && !!local;
              return (
                <tr key={s.key} data-testid={`status-row-${s.key}`}>
                  <td className="order-cell">
                    <button className="ghost" disabled={readOnly || i === 0} aria-label={`Move ${s.name} left`} onClick={() => move(s.key, -1)}>↑</button>
                    <button className="ghost" disabled={readOnly || i === sv.resolved.length - 1} aria-label={`Move ${s.name} right`} onClick={() => move(s.key, 1)}>↓</button>
                  </td>
                  <td>
                    <input aria-label={`Name of ${s.key}`} defaultValue={s.name} disabled={readOnly} key={s.name}
                      onBlur={(e) => e.target.value !== s.name && run(`Renamed ${s.key}`, () => api.patchStatus(node.id, s.key, { name: e.target.value }))} />
                  </td>
                  <td>
                    <select aria-label={`Category of ${s.key}`} value={s.category} disabled={readOnly}
                      onChange={(e) => run(`Updated ${s.key}`, () => api.patchStatus(node.id, s.key, { category: e.target.value }))}>
                      {CATEGORIES.map((c) => <option key={c} value={c}>{catLabel[c]}</option>)}
                    </select>
                  </td>
                  <td>
                    <input type="color" aria-label={`Color of ${s.key}`} defaultValue={s.color} disabled={readOnly} key={s.color}
                      onBlur={(e) => e.target.value.toLowerCase() !== s.color.toLowerCase() && run(`Recolored ${s.key}`, () => api.patchStatus(node.id, s.key, { color: e.target.value }))} />
                  </td>
                  <td className="source">
                    {definedHere ? "Defined here" : `${s.ownerType === "workspace" ? "Workspace" : `The ${s.ownerType}`} default`}
                    {overriddenHere && <span className="tag">overridden here</span>}
                  </td>
                  <td className="row-actions">
                    {!readOnly && definedHere && (
                      <button className="ghost danger" onClick={() => run(`Deleted ${s.name}`, (r) => api.deleteStatus(node.id, s.key, r))}>Delete</button>
                    )}
                    {!readOnly && overriddenHere && (
                      <button className="ghost" onClick={() => run(`Reverted ${s.name}`, (r) => api.deleteStatus(node.id, s.key, r))}>Revert</button>
                    )}
                    {!readOnly && !definedHere && (
                      <button className="ghost" onClick={() => run(`Hid ${s.name}`, (r) => api.patchStatus(node.id, s.key, { hidden: true }, r))}>Hide here</button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {hiddenHere.length > 0 && (
          <p className="hidden-note">
            Hidden here: {hiddenHere.map((d) => (
              <button key={d.key} className="chip" disabled={readOnly}
                onClick={() => run(`Unhid ${d.key}`, () => api.patchStatus(node.id, d.key, { hidden: false }))}>Show {d.key}</button>
            ))}
          </p>
        )}
        {!readOnly && <AddStatus onAdd={(body) => run(`Added ${body.name}`, () => api.createStatus(node.id, body))} />}
      </section>

      <section className="config-section">
        <header>
          <h2>Custom fields</h2>
          <p>Fields defined here appear on every task under {scopeName}. A key can only be defined once along any branch.</p>
        </header>
        {node.type === "workspace" ? (
          <p className="empty-note">Custom fields live on spaces, folders and lists. Pick one in the sidebar.</p>
        ) : (
          <>
            <table className="config-table">
              <thead><tr><th>Name</th><th>Key</th><th>Type</th><th>Required</th><th>Comes from</th><th /></tr></thead>
              <tbody>
                {fv.resolved.map((f) => {
                  const here = f.ownerId === node.id;
                  return (
                    <tr key={f.key} data-testid={`field-row-${f.key}`}>
                      <td>{f.name}{f.options.length > 0 && <span className="opts">{f.options.map((o) => o.label).join(" / ")}</span>}</td>
                      <td><code>{f.key}</code></td>
                      <td>{typeLabel[f.type]}</td>
                      <td>
                        <input type="checkbox" aria-label={`${f.name} required`} checked={f.required} disabled={readOnly || !here}
                          onChange={(e) => toggleRequired(f.id, f.name, e.target.checked)} />
                      </td>
                      <td className="source">{here ? "Defined here" : `The ${f.ownerType}`}</td>
                      <td className="row-actions">
                        {!readOnly && here && (
                          <button className="ghost danger" onClick={() => {
                            if (confirm(`Delete “${f.name}”? Its values are removed from every task under ${scopeName}.`)) {
                              run(`Deleted ${f.name}`, () => api.deleteField(node.id, f.id));
                            }
                          }}>Delete</button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            {fv.resolved.length === 0 && <p className="empty-note">No custom fields apply here yet.</p>}
            {!readOnly && <AddField onAdd={(body) => run(`Added ${body.name}`, () => api.createField(node.id, body))} />}
          </>
        )}
      </section>
    </div>
  );

  async function toggleRequired(id: string, name: string, required: boolean) {
    try {
      await api.patchField(node.id, id, { required });
      await load();
      onChanged(`${name} is now ${required ? "required" : "optional"}`);
    } catch (e) {
      if (e instanceof ApiError && e.code === "FIELD_REQUIRED_BACKFILL") {
        const v = prompt(`${e.message}\n\nValue to fill in for those tasks:`);
        if (v === null) return;
        let parsed: unknown = v;
        try { parsed = JSON.parse(v); } catch { /* keep as string */ }
        await run(`${name} is now required`, () => api.patchField(node.id, id, { required, defaultValue: parsed }));
      } else {
        setError(e instanceof ApiError ? e.message : String(e));
      }
    }
  }
}

function AddStatus({ onAdd }: { onAdd: (b: { key: string; name: string; category: string; color: string }) => void }) {
  const [name, setName] = useState("");
  const [category, setCategory] = useState<StatusCategory>("active");
  const [color, setColor] = useState("#64748b");
  return (
    <form className="add-row" onSubmit={(e) => { e.preventDefault(); if (name) { onAdd({ key: slug(name), name, category, color }); setName(""); } }}>
      <input aria-label="New status name" placeholder="New status name" value={name} onChange={(e) => setName(e.target.value)} />
      <select aria-label="New status category" value={category} onChange={(e) => setCategory(e.target.value as StatusCategory)}>
        {CATEGORIES.map((c) => <option key={c} value={c}>{catLabel[c]}</option>)}
      </select>
      <input type="color" aria-label="New status color" value={color} onChange={(e) => setColor(e.target.value)} />
      <button className="secondary" disabled={!name}>Add status</button>
    </form>
  );
}

function AddField({ onAdd }: { onAdd: (b: { key: string; name: string; type: FieldType; required: boolean; options?: { id: string; label: string }[] }) => void }) {
  const [name, setName] = useState("");
  const [type, setType] = useState<FieldType>("text");
  const [options, setOptions] = useState("");
  const [required, setRequired] = useState(false);
  const isSelect = type === "single_select" || type === "multi_select";
  return (
    <form className="add-row" onSubmit={(e) => {
      e.preventDefault();
      if (!name) return;
      const opts = isSelect ? options.split(",").map((s) => s.trim()).filter(Boolean).map((label) => ({ id: slug(label).replace(/_/g, "-"), label })) : undefined;
      onAdd({ key: slug(name), name, type, required, options: opts });
      setName(""); setOptions(""); setRequired(false);
    }}>
      <input aria-label="New field name" placeholder="New field name" value={name} onChange={(e) => setName(e.target.value)} />
      <select aria-label="New field type" value={type} onChange={(e) => setType(e.target.value as FieldType)}>
        {FIELD_TYPES.map((t) => <option key={t} value={t}>{typeLabel[t]}</option>)}
      </select>
      {isSelect && <input aria-label="Options" placeholder="Options, comma separated" value={options} onChange={(e) => setOptions(e.target.value)} />}
      <label className="inline"><input type="checkbox" checked={required} onChange={(e) => setRequired(e.target.checked)} /> Required</label>
      <button className="secondary" disabled={!name}>Add field</button>
    </form>
  );
}
