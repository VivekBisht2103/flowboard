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

// Muted, distinguishable avatar tones; the same user always gets the same one.
const AVATAR_TONES = [
  ["#e8eefc", "#2f4fb3"], ["#e6f4ef", "#1f7a55"], ["#fbefe6", "#a4521b"],
  ["#f1ebfb", "#6a3fb0"], ["#e7f3f8", "#1c6f8c"], ["#fbeaec", "#a3343f"],
];

export function avatarStyle(id: string): { background: string; color: string } {
  let h = 0;
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  const [background, color] = AVATAR_TONES[h % AVATAR_TONES.length];
  return { background, color };
}
