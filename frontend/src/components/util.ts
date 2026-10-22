import type { FieldDef, FieldValue, Priority, TreeNode, User } from "../api/types";

export const priorityLabel: Record<Priority, string> = {
  urgent: "Urgent", high: "High", normal: "Normal", low: "Low", none: "No priority",
};

export function initials(name: string): string {
  return name.split(/\s+/).map((p) => p[0]).slice(0, 2).join("").toUpperCase();
}

export function userName(users: User[], id: string): string {
  return users.find((u) => u.id === id)?.name ?? id;
}

export function shortDate(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
}

export function isOverdue(iso: string | null): boolean {
  return !!iso && new Date(iso).getTime() < Date.now();
}

export function formatFieldValue(def: FieldDef, v: FieldValue | undefined): string {
  if (v === undefined || v === null) return "";
  switch (def.type) {
    case "single_select":
      return def.options.find((o) => o.id === v)?.label ?? String(v);
    case "multi_select":
      return (v as string[]).map((id) => def.options.find((o) => o.id === id)?.label ?? id).join(", ");
    case "checkbox":
      return v ? "Yes" : "No";
    case "date":
      return shortDate(String(v) + "T00:00:00Z");
    default:
      return String(v);
  }
}

export function findNode(root: TreeNode | null, id: string | null): TreeNode | null {
  if (!root || !id) return null;
  if (root.id === id) return root;
  for (const c of root.children) {
    const f = findNode(c, id);
    if (f) return f;
  }
  return null;
}

export function pathTo(root: TreeNode | null, id: string): TreeNode[] {
  if (!root) return [];
  if (root.id === id) return [root];
  for (const c of root.children) {
    const p = pathTo(c, id);
    if (p.length) return [root, ...p];
  }
  return [];
}

export function firstList(root: TreeNode | null): TreeNode | null {
  if (!root) return null;
  if (root.type === "list") return root;
  for (const c of root.children) {
    const f = firstList(c);
    if (f) return f;
  }
  return null;
}
