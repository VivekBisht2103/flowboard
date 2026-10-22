// Shared API contract types (mirror of the Go /v1 JSON shapes).

export type UserId = string;

export interface User {
  id: UserId;
  name: string;
  email: string;
  role: "admin" | "member";
}

export type ContainerType = "workspace" | "space" | "folder" | "list";

export interface TreeNode {
  id: string;
  workspaceId: string;
  type: ContainerType;
  parentId: string | null;
  name: string;
  position: number;
  visibility: "public" | "private";
  children: TreeNode[];
}

export type StatusCategory = "not_started" | "active" | "done" | "closed";

export interface Status {
  id: string;
  key: string;
  name: string;
  category: StatusCategory;
  color: string;
  position: number;
  ownerType: ContainerType;
  ownerId: string;
  overriddenAt?: string;
  inherited: boolean;
}

export interface StatusDef {
  id: string;
  ownerId: string;
  key: string;
  name: string | null;
  category: StatusCategory | null;
  color: string | null;
  position: number | null;
  hidden: boolean;
}

export interface StatusView {
  containerId: string;
  containerType: ContainerType;
  local: StatusDef[];
  inherited: Status[];
  resolved: Status[];
}

export type FieldType = "text" | "number" | "single_select" | "multi_select" | "date" | "checkbox";

export interface FieldOption {
  id: string;
  label: string;
  color?: string;
}

export interface FieldDef {
  id: string;
  ownerId: string;
  ownerType: ContainerType;
  key: string;
  name: string;
  type: FieldType;
  required: boolean;
  options: FieldOption[];
  position: number;
  inherited: boolean;
}

export interface FieldView {
  containerId: string;
  containerType: ContainerType;
  local: FieldDef[];
  inherited: FieldDef[];
  resolved: FieldDef[];
}

export type Priority = "urgent" | "high" | "normal" | "low" | "none";
export const PRIORITIES: Priority[] = ["urgent", "high", "normal", "low", "none"];

export type FieldValue = string | number | boolean | string[];

export interface Task {
  id: string;
  listId: string;
  title: string;
  description: string;
  statusKey: string;
  priority: Priority;
  assigneeIds: string[];
  dueDate: string | null;
  customFields: Record<string, FieldValue>;
  position: number;
  version: number;
  createdBy: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface Column {
  status: Status;
  tasks: Task[];
  count: number;
}

export interface Board {
  listId: string;
  statuses: Status[];
  fields: FieldDef[];
  columns: Column[];
  unmapped: number;
  truncated: boolean;
}

export interface Page<T> {
  data: T[];
  page: { nextCursor: string | null; limit: number };
}

export interface Activity {
  id: number;
  type: string;
  summary: string;
  actorId: string | null;
  createdAt: string;
}

export interface FieldError {
  path: string;
  code: string;
  message: string;
}
