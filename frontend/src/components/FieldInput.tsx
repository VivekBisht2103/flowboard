import type { FieldDef, FieldValue } from "../api/types";

interface Props {
  def: FieldDef;
  value: FieldValue | undefined;
  onChange: (v: FieldValue | null) => void;
  error?: string;
}

/** Renders the right control for a custom field type. null means "clear". */
export function FieldInput({ def, value, onChange, error }: Props) {
  const id = `cf-${def.key}`;
  const label = (
    <label htmlFor={id} className="field-label">
      {def.name}
      {def.required && <span className="req" aria-label="required">*</span>}
      {def.inherited && <span className="scope">from {def.ownerType}</span>}
    </label>
  );
  let control: JSX.Element;
  switch (def.type) {
    case "text":
      control = (
        <input id={id} type="text" value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value === "" ? null : e.target.value)} />
      );
      break;
    case "number":
      control = (
        <input id={id} type="number" step="any" value={value === undefined ? "" : String(value)}
          onChange={(e) => onChange(e.target.value === "" ? null : Number(e.target.value))} />
      );
      break;
    case "date":
      control = (
        <input id={id} type="date" value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value || null)} />
      );
      break;
    case "checkbox":
      control = (
        <input id={id} type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked)} />
      );
      break;
    case "single_select":
      control = (
        <select id={id} value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value || null)}>
          <option value="">—</option>
          {def.options.map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
        </select>
      );
      break;
    case "multi_select": {
      const selected = (value as string[]) ?? [];
      control = (
        <div id={id} className="chips" role="group" aria-label={def.name}>
          {def.options.map((o) => {
            const on = selected.includes(o.id);
            return (
              <button type="button" key={o.id} className={`chip ${on ? "on" : ""}`} aria-pressed={on}
                style={on && o.color ? { borderColor: o.color, color: o.color } : undefined}
                onClick={() => {
                  const next = on ? selected.filter((s) => s !== o.id) : [...selected, o.id];
                  onChange(next.length ? next : null);
                }}>
                {o.label}
              </button>
            );
          })}
        </div>
      );
      break;
    }
  }
  return (
    <div className={`field ${def.type === "checkbox" ? "field-inline" : ""} ${error ? "has-error" : ""}`}>
      {label}
      {control}
      {error && <p className="field-error" role="alert">{error}</p>}
    </div>
  );
}
