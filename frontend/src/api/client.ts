import type {
  Activity, Board, FieldDef, FieldError, FieldValue, FieldView, Page, StatusView, Task, TreeNode, User,
} from "./types";

/** Error carrying the standard { error: { code, message, details } } envelope. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details?: { fieldErrors?: FieldError[]; [k: string]: unknown },
  ) {
    super(message);
  }
  get fieldErrors(): FieldError[] {
    return this.details?.fieldErrors ?? [];
  }
}

const BASE = (import.meta.env?.VITE_API_URL as string | undefined) ?? "";

let currentUser = "alice";
export function setApiUser(id: string) {
  currentUser = id;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${BASE}/v1${path}`, {
    method,
    headers: { "Content-Type": "application/json", "X-User-Id": currentUser },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return undefined as T;
  const json = await res.json().catch(() => ({}));
  if (!res.ok) {
    const e = json?.error ?? {};
    throw new ApiError(res.status, e.code ?? "UNKNOWN", e.message ?? `HTTP ${res.status}`, e.details);
  }
  return json as T;
}

export interface TaskPatch {
  title?: string;
  description?: string;
  statusKey?: string;
  priority?: string;
  assigneeIds?: string[];
  dueDate?: string | null;
  customFields?: Record<string, FieldValue | null>;
  version?: number;
}

export interface MovePayload {
  listId?: string;
  statusKey?: string;
  beforeTaskId?: string;
  afterTaskId?: string;
  statusPolicy?: "map_to_default" | "reject";
  customFields?: Record<string, FieldValue | null>;
  version?: number;
}

export const api = {
  me: () => request<User>("GET", "/me"),
  users: () => request<{ data: User[] }>("GET", "/users").then((r) => r.data),
  tree: () => request<TreeNode>("GET", "/tree"),

  board: (listId: string, filters: Record<string, string> = {}) =>
    request<Board>("GET", `/lists/${listId}/board${qs(filters)}`),
  tasks: (listId: string, params: Record<string, string>) =>
    request<Page<Task>>("GET", `/lists/${listId}/tasks${qs(params)}`),
  createTask: (listId: string, body: TaskPatch & { title: string }) =>
    request<Task>("POST", `/lists/${listId}/tasks`, body),
  task: (id: string) => request<Task>("GET", `/tasks/${id}`),
  updateTask: (id: string, patch: TaskPatch) => request<Task>("PATCH", `/tasks/${id}`, patch),
  deleteTask: (id: string) => request<void>("DELETE", `/tasks/${id}`),
  moveTask: (id: string, body: MovePayload) =>
    request<{ task: Task; effects: { statusMapped: { from: string; to: string } | null; strippedFields: string[] } }>(
      "POST", `/tasks/${id}/move`, body),
  taskActivity: (id: string) => request<Page<Activity>>("GET", `/tasks/${id}/activity`),

  statusView: (containerId: string) => request<StatusView>("GET", `/containers/${containerId}/statuses`),
  createStatus: (containerId: string, body: Record<string, unknown>) =>
    request("POST", `/containers/${containerId}/statuses`, body),
  patchStatus: (containerId: string, key: string, body: Record<string, unknown>, remapTo?: string) =>
    request("PATCH", `/containers/${containerId}/statuses/${key}${qs(remapTo ? { remapTo } : {})}`, body),
  deleteStatus: (containerId: string, key: string, remapTo?: string) =>
    request("DELETE", `/containers/${containerId}/statuses/${key}${qs(remapTo ? { remapTo } : {})}`),

  reorderStatuses: (containerId: string, keys: string[]) =>
    request("PUT", `/containers/${containerId}/statuses/order`, { keys }),

  fieldView: (containerId: string) => request<FieldView>("GET", `/containers/${containerId}/fields`),
  createField: (containerId: string, body: Partial<FieldDef> & { defaultValue?: FieldValue }) =>
    request("POST", `/containers/${containerId}/fields`, body),
  patchField: (containerId: string, id: string, body: Record<string, unknown>) =>
    request("PATCH", `/containers/${containerId}/fields/${id}`, body),
  deleteField: (containerId: string, id: string) => request("DELETE", `/containers/${containerId}/fields/${id}`),
};

function qs(params: Record<string, string>): string {
  const entries = Object.entries(params).filter(([, v]) => v !== "" && v !== undefined);
  return entries.length ? "?" + new URLSearchParams(entries).toString() : "";
}
