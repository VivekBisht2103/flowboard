import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError, setApiUser } from "./api/client";
import type { Board as BoardData, TreeNode, User } from "./api/types";
import { Board } from "./components/Board";
import { ConfigPanel } from "./components/ConfigPanel";
import { ListView } from "./components/ListView";
import { Sidebar } from "./components/Sidebar";
import { TaskDrawer } from "./components/TaskDrawer";
import { findNode, firstList, pathTo } from "./components/util";

type View = "board" | "list" | "settings";
type DrawerState = { taskId?: string; createStatusKey?: string } | null;

const USER_KEY = "flowboard.user";

function readStored(key: string): string | null {
  try { return localStorage.getItem(key); } catch { return null; }
}
function writeStored(key: string, v: string) {
  try { localStorage.setItem(key, v); } catch { /* storage unavailable */ }
}

function collectLists(n: TreeNode | null, out: { id: string; name: string }[] = []) {
  if (!n) return out;
  if (n.type === "list") out.push({ id: n.id, name: n.name });
  n.children.forEach((c) => collectLists(c, out));
  return out;
}

export default function App() {
  const [userId, setUserId] = useState(() => readStored(USER_KEY) ?? "priya");
  const [me, setMe] = useState<User | null>(null);
  const [users, setUsers] = useState<User[]>([]);
  const [tree, setTree] = useState<TreeNode | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [view, setView] = useState<View>("board");
  const [board, setBoard] = useState<BoardData | null>(null);
  const [boardError, setBoardError] = useState("");
  const [drawer, setDrawer] = useState<DrawerState>(null);
  const [toast, setToast] = useState("");
  const [refreshKey, setRefreshKey] = useState(0);

  const notify = useCallback((msg: string) => {
    setToast(msg);
    window.setTimeout(() => setToast((t) => (t === msg ? "" : t)), 4000);
  }, []);

  // Identity + tree: reload whenever the acting user changes.
  useEffect(() => {
    setApiUser(userId);
    writeStored(USER_KEY, userId);
    let cancelled = false;
    Promise.all([api.me(), api.users(), api.tree()])
      .then(([m, us, t]) => {
        if (cancelled) return;
        setMe(m);
        setUsers(us);
        setTree(t);
        setSelectedId((cur) => (cur && findNode(t, cur) ? cur : firstList(t)?.id ?? t.id));
      })
      .catch((e) => notify(e instanceof ApiError ? e.message : "Could not reach the API."));
    return () => { cancelled = true; };
  }, [userId, notify]);

  const selected = useMemo(() => findNode(tree, selectedId), [tree, selectedId]);
  const isList = selected?.type === "list";
  const effectiveView: View = isList ? view : "settings";

  const loadBoard = useCallback(() => {
    if (!selected || selected.type !== "list") { setBoard(null); return; }
    setBoardError("");
    api.board(selected.id)
      .then(setBoard)
      .catch((e) => { setBoard(null); setBoardError(e instanceof ApiError ? e.message : "Could not load the board."); });
  }, [selected]);

  useEffect(loadBoard, [loadBoard]);

  const afterChange = (msg: string) => {
    notify(msg);
    loadBoard();
    setRefreshKey((k) => k + 1);
  };

  const crumbs = selected ? pathTo(tree, selected.id) : [];

  return (
    <div className="app">
      <aside className="rail">
        <div className="brand">
          <span className="brand-mark" aria-hidden>F</span>
          <span className="brand-name">Flowboard</span>
        </div>
        <Sidebar tree={tree} selectedId={selectedId} onSelect={(n) => { setSelectedId(n.id); if (n.type !== "list") setView("settings"); else if (view === "settings") setView("board"); }} />
        <label className="acting-as">
          <span>Acting as</span>
          <select aria-label="Acting as" value={userId} onChange={(e) => setUserId(e.target.value)}>
            {(users.length ? users : [{ id: "priya", name: "Priya Sharma" }, { id: "rahul", name: "Rahul Verma" }, { id: "ananya", name: "Ananya Iyer" }] as User[])
              .map((u) => <option key={u.id} value={u.id}>{u.name}{u.role ? ` (${u.role})` : ""}</option>)}
          </select>
        </label>
      </aside>

      <main className="main">
        <header className="topbar">
          <nav aria-label="Breadcrumb" className="crumbs">
            {crumbs.map((c, i) => (
              <span key={c.id}>
                {i > 0 && <span className="sep" aria-hidden>/</span>}
                <button className={i === crumbs.length - 1 ? "current" : ""} onClick={() => setSelectedId(c.id)}>{c.name}</button>
              </span>
            ))}
          </nav>
          <div className="tabs" role="tablist" aria-label="View">
            {isList && (["board", "list"] as View[]).map((v) => (
              <button key={v} role="tab" aria-selected={effectiveView === v} onClick={() => setView(v)}>
                {v === "board" ? "Board" : "List"}
              </button>
            ))}
            <button role="tab" aria-selected={effectiveView === "settings"} onClick={() => setView("settings")}>Statuses & fields</button>
          </div>
          {isList && effectiveView !== "settings" && board && (
            <button className="primary new-task" onClick={() => setDrawer({ createStatusKey: board.statuses[0]?.key })}>New task</button>
          )}
        </header>

        <div className="content">
          {!selected && <p className="empty-note">Select a list in the sidebar to see its board.</p>}
          {boardError && isList && effectiveView !== "settings" && <p className="banner">{boardError}</p>}
          {isList && board && effectiveView === "board" && (
            <Board board={board} users={users} onError={notify}
              onOpenTask={(id) => setDrawer({ taskId: id })}
              onCreate={(k) => setDrawer({ createStatusKey: k })} />
          )}
          {isList && board && effectiveView === "list" && (
            <ListView listId={selected!.id} statuses={board.statuses} fields={board.fields} users={users}
              refreshKey={refreshKey} onError={notify} onOpenTask={(id) => setDrawer({ taskId: id })} />
          )}
          {selected && effectiveView === "settings" && (
            <ConfigPanel node={selected} isAdmin={me?.role === "admin"} onChanged={afterChange} />
          )}
        </div>
      </main>

      {drawer && board && selected && (
        <TaskDrawer listId={selected.id} statuses={board.statuses} fields={board.fields} users={users}
          lists={collectLists(tree)} taskId={drawer.taskId} createStatusKey={drawer.createStatusKey}
          onClose={() => setDrawer(null)}
          onSaved={(msg) => { afterChange(msg); if (!drawer.taskId) setDrawer(null); }} />
      )}

      <div className="toast" role="status" aria-live="polite">{toast}</div>
    </div>
  );
}
