// A tiny in-memory stand-in for the /v1 API, used by component tests.
// It mirrors the real contract closely enough to exercise the UI flows and records
// every request so tests can assert on payloads.
import type { Board, FieldDef, Status, Task, TreeNode, User } from "../api/types";

const users: User[] = [
  { id: "alice", name: "Alice Admin", email: "a@x", role: "admin" },
  { id: "bob", name: "Bob Builder", email: "b@x", role: "member" },
  { id: "carol", name: "Carol Chen", email: "c@x", role: "member" },
];

const st = (key: string, name: string, category: Status["category"], color: string, position: number): Status => ({
  id: key, key, name, category, color, position, ownerType: "workspace", ownerId: "ws", inherited: true,
});

const statusesByList: Record<string, Status[]> = {
  lst_backlog: [st("todo", "To Do", "not_started", "#94a3b8", 1), st("in_progress", "In Progress", "active", "#3b82f6", 2),
    st("in_review", "In Review", "active", "#a855f7", 3), st("done", "Done", "done", "#22c55e", 4)],
  lst_sprint: [st("todo", "Ready", "not_started", "#0ea5e9", 1), st("blocked", "Blocked", "active", "#ef4444", 2),
    st("in_progress", "In Progress", "active", "#3b82f6", 3), st("qa", "QA", "active", "#f59e0b", 4), st("done", "Done", "done", "#22c55e", 5)],
  lst_security: [st("todo", "To Do", "not_started", "#94a3b8", 1), st("in_progress", "In Progress", "active", "#3b82f6", 2), st("done", "Done", "done", "#22c55e", 3)],
};

const fields: FieldDef[] = [
  { id: "f1", ownerId: "spc_eng", ownerType: "space", key: "component", name: "Component", type: "single_select", required: true,
    options: [{ id: "frontend", label: "Frontend" }, { id: "backend", label: "Backend" }], position: 1, inherited: true },
  { id: "f2", ownerId: "spc_eng", ownerType: "space", key: "story_points", name: "Story Points", type: "number", required: false,
    options: [], position: 2, inherited: true },
];

const task = (id: string, listId: string, title: string, statusKey: string, cf: Task["customFields"]): Task => ({
  id, listId, title, description: "", statusKey, priority: "normal", assigneeIds: ["bob"], dueDate: null,
  customFields: cf, position: 1, version: 1, createdBy: "alice", createdAt: "", updatedAt: "",
});

export function createFakeApi() {
  const tasks: Task[] = [
    task("t1", "lst_backlog", "Backlog item", "in_review", { component: "backend" }),
    task("t2", "lst_sprint", "Sprint item", "qa", { component: "frontend", story_points: 3 }),
    task("t3", "lst_security", "Secret item", "todo", { component: "backend" }),
  ];
  const requests: { method: string; path: string; user: string; body?: unknown }[] = [];

  const tree = (user: string): TreeNode => {
    const node = (id: string, type: TreeNode["type"], name: string, children: TreeNode[] = [], visibility: "public" | "private" = "public"): TreeNode =>
      ({ id, workspaceId: "ws", type, parentId: null, name, position: 0, visibility, children });
    const lists = [node("lst_backlog", "list", "Backlog"), node("lst_sprint", "list", "Sprint 12")];
    if (user !== "bob") lists.push(node("lst_security", "list", "Security Audit", [], "private"));
    return node("ws", "workspace", "Acme", [node("spc_eng", "space", "Engineering", [node("fld_q2", "folder", "Q2 Launch", lists)])]);
  };

  const json = (status: number, body: unknown) =>
    new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

  const fetchImpl = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(String(input), "http://test");
    const path = url.pathname.replace(/^\/v1/, "");
    const method = init?.method ?? "GET";
    const user = new Headers(init?.headers).get("X-User-Id") ?? "";
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    requests.push({ method, path, user, body });
    if (!users.some((u) => u.id === user)) return json(401, { error: { code: "UNAUTHENTICATED", message: "unknown user" } });

    let m: RegExpMatchArray | null;
    if (path === "/me") return json(200, users.find((u) => u.id === user));
    if (path === "/users") return json(200, { data: users });
    if (path === "/tree") return json(200, tree(user));
    if ((m = path.match(/^\/lists\/([^/]+)\/board$/))) {
      const listId = m[1];
      if (user === "bob" && listId === "lst_security") return json(403, { error: { code: "FORBIDDEN", message: "no access" } });
      const statuses = statusesByList[listId];
      const board: Board = {
        listId, statuses, fields, unmapped: 0, truncated: false,
        columns: statuses.map((s) => {
          const ts = tasks.filter((t) => t.listId === listId && t.statusKey === s.key);
          return { status: s, tasks: ts, count: ts.length };
        }),
      };
      return json(200, board);
    }
    if ((m = path.match(/^\/tasks\/([^/]+)\/activity$/))) return json(200, { data: [], page: { nextCursor: null, limit: 30 } });
    if ((m = path.match(/^\/tasks\/([^/]+)$/))) {
      const t = tasks.find((x) => x.id === m![1]);
      if (!t) return json(404, { error: { code: "NOT_FOUND", message: "task not found" } });
      if (method === "GET") return json(200, t);
      if (method === "PATCH") {
        const patch = body as Partial<Task> & { customFields?: Record<string, unknown> };
        const cf = { ...t.customFields } as Record<string, unknown>;
        for (const [k, v] of Object.entries(patch.customFields ?? {})) {
          if (k === "story_points" && v !== null && typeof v !== "number") {
            return json(400, { error: { code: "VALIDATION_FAILED", message: "bad", details: { fieldErrors: [{ path: "customFields.story_points", code: "INVALID_TYPE", message: "expected a number" }] } } });
          }
          if (k === "component" && v === null) {
            return json(400, { error: { code: "VALIDATION_FAILED", message: "bad", details: { fieldErrors: [{ path: "customFields.component", code: "REQUIRED", message: "Component is required" }] } } });
          }
          if (v === null) delete cf[k]; else cf[k] = v;
        }
        Object.assign(t, { ...patch, customFields: cf, version: t.version + 1 });
        return json(200, t);
      }
    }
    return json(404, { error: { code: "NOT_FOUND", message: `no fake for ${method} ${path}` } });
  };

  return { fetch: fetchImpl, requests, tasks };
}
