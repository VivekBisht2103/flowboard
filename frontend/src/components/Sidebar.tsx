import { useState } from "react";
import type { TreeNode } from "../api/types";

interface Props {
  tree: TreeNode | null;
  selectedId: string | null;
  onSelect: (node: TreeNode) => void;
}

const glyph: Record<string, string> = { space: "◆", folder: "▤", list: "≡" };

export function Sidebar({ tree, selectedId, onSelect }: Props) {
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  if (!tree) return <nav className="sidebar" aria-label="Workspace" />;

  const render = (n: TreeNode, depth: number) => {
    const hasKids = n.children.length > 0;
    const isOpen = !collapsed[n.id];
    return (
      <li key={n.id} role="treeitem" aria-expanded={hasKids ? isOpen : undefined} aria-selected={selectedId === n.id}>
        <div className={`tree-row type-${n.type} ${selectedId === n.id ? "selected" : ""}`} style={{ paddingLeft: 8 + depth * 14 }}>
          {hasKids ? (
            <button className="twisty" aria-label={isOpen ? `Collapse ${n.name}` : `Expand ${n.name}`}
              onClick={() => setCollapsed((c) => ({ ...c, [n.id]: isOpen }))}>
              {isOpen ? "▾" : "▸"}
            </button>
          ) : <span className="twisty-spacer" />}
          <button className="tree-label" onClick={() => onSelect(n)}>
            <span className="glyph" aria-hidden>{glyph[n.type]}</span>
            <span className="name">{n.name}</span>
            {n.visibility === "private" && <span className="lock" title="Private">private</span>}
          </button>
        </div>
        {hasKids && isOpen && <ul role="group">{n.children.map((c) => render(c, depth + 1))}</ul>}
      </li>
    );
  };

  return (
    <nav className="sidebar" aria-label="Workspace">
      <button className={`workspace-name ${selectedId === tree.id ? "selected" : ""}`} onClick={() => onSelect(tree)}>
        {tree.name}
      </button>
      <ul role="tree" aria-label="Spaces, folders and lists">
        {tree.children.map((c) => render(c, 0))}
      </ul>
      {tree.children.length === 0 && <p className="empty-note">No spaces are shared with you yet.</p>}
    </nav>
  );
}
