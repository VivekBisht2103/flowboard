import { useEffect, useState } from "react";
import {
  DndContext, DragOverlay, KeyboardSensor, PointerSensor, useDraggable, useDroppable, useSensor, useSensors,
  type DragEndEvent, type DragStartEvent,
} from "@dnd-kit/core";
import { api, ApiError } from "../api/client";
import type { Board as BoardData, Column, FieldDef, Task, User } from "../api/types";
import { avatarStyle, formatFieldValue, initials, isOverdue, priorityLabel, shortDate, userName } from "./util";

interface Props {
  board: BoardData;
  users: User[];
  onOpenTask: (id: string) => void;
  onCreate: (statusKey: string) => void;
  onError: (msg: string) => void;
}

/** Applies a move to column state locally (optimistic UI). */
export function applyMove(cols: Column[], taskId: string, toKey: string, beforeId?: string): Column[] {
  let moving: Task | undefined;
  const without = cols.map((c) => {
    const idx = c.tasks.findIndex((t) => t.id === taskId);
    if (idx === -1) return c;
    moving = c.tasks[idx];
    return { ...c, tasks: c.tasks.filter((t) => t.id !== taskId), count: c.count - 1 };
  });
  if (!moving) return cols;
  const moved = { ...moving, statusKey: toKey };
  return without.map((c) => {
    if (c.status.key !== toKey) return c;
    const tasks = [...c.tasks];
    const at = beforeId ? tasks.findIndex((t) => t.id === beforeId) : -1;
    tasks.splice(at === -1 ? tasks.length : at, 0, moved);
    return { ...c, tasks, count: c.count + 1 };
  });
}

export function Board({ board, users, onOpenTask, onCreate, onError }: Props) {
  const [columns, setColumns] = useState(board.columns);
  const [dragging, setDragging] = useState<Task | null>(null);
  useEffect(() => setColumns(board.columns), [board]);

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor),
  );
  // The card badge shows the first single-select field (e.g. Component).
  const badgeField = board.fields.find((f) => f.type === "single_select");

  const onDragStart = (e: DragStartEvent) => {
    setDragging(columns.flatMap((c) => c.tasks).find((t) => t.id === e.active.id) ?? null);
  };

  const onDragEnd = async (e: DragEndEvent) => {
    setDragging(null);
    const over = e.over?.data.current as { statusKey: string; taskId?: string } | undefined;
    if (!over) return;
    const taskId = String(e.active.id);
    const beforeId = over.taskId && over.taskId !== taskId ? over.taskId : undefined;
    const from = columns.find((c) => c.tasks.some((t) => t.id === taskId));
    if (!from || (from.status.key === over.statusKey && !beforeId && from.tasks.at(-1)?.id === taskId)) return;

    const previous = columns;
    setColumns(applyMove(columns, taskId, over.statusKey, beforeId)); // optimistic
    try {
      const res = await api.moveTask(taskId, { statusKey: over.statusKey, beforeTaskId: beforeId });
      setColumns((cols) => cols.map((c) => ({ ...c, tasks: c.tasks.map((t) => (t.id === taskId ? res.task : t)) })));
    } catch (err) {
      setColumns(previous); // roll back
      onError(err instanceof ApiError ? err.message : "Could not move the task. Check your connection and try again.");
    }
  };

  return (
    <DndContext sensors={sensors} onDragStart={onDragStart} onDragEnd={onDragEnd} onDragCancel={() => setDragging(null)}>
      <div className="board" role="region" aria-label="Kanban board">
        {columns.map((col) => (
          <BoardColumn key={col.status.key} column={col} users={users} badgeField={badgeField}
            onOpenTask={onOpenTask} onCreate={onCreate} />
        ))}
      </div>
      <DragOverlay dropAnimation={null}>
        {dragging && <CardBody task={dragging} users={users} badgeField={badgeField} lifted />}
      </DragOverlay>
    </DndContext>
  );
}

function BoardColumn({ column, users, badgeField, onOpenTask, onCreate }: {
  column: Column; users: User[]; badgeField?: FieldDef; onOpenTask: (id: string) => void; onCreate: (k: string) => void;
}) {
  const { setNodeRef, isOver } = useDroppable({ id: `col:${column.status.key}`, data: { statusKey: column.status.key } });
  const s = column.status;
  return (
    <section className={`column ${isOver ? "over" : ""}`} aria-label={`${s.name} column`} data-testid={`column-${s.key}`}
      style={{ ["--status" as string]: s.color }}>
      <header className="column-head">
        <span className="status-dot" aria-hidden />
        <h3>{s.name}</h3>
        <span className="count">{column.count}</span>
        <button className="ghost add" onClick={() => onCreate(s.key)} aria-label={`Add task to ${s.name}`}>+</button>
      </header>
      <div ref={setNodeRef} className="column-body">
        {column.tasks.map((t) => (
          <DraggableCard key={t.id} task={t} users={users} badgeField={badgeField} onOpen={() => onOpenTask(t.id)} />
        ))}
        {column.tasks.length === 0 && <p className="column-empty">Drop a card here</p>}
      </div>
    </section>
  );
}

function DraggableCard({ task, users, badgeField, onOpen }: { task: Task; users: User[]; badgeField?: FieldDef; onOpen: () => void }) {
  const drag = useDraggable({ id: task.id });
  const drop = useDroppable({ id: `card:${task.id}`, data: { statusKey: task.statusKey, taskId: task.id } });
  return (
    <div ref={(el) => { drag.setNodeRef(el); drop.setNodeRef(el); }}
      className={`card-slot ${drop.isOver ? "insert-before" : ""} ${drag.isDragging ? "ghosted" : ""}`}
      {...drag.attributes} {...drag.listeners}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === "Enter") onOpen();
        drag.listeners?.onKeyDown?.(e);
      }}
      aria-label={`Open ${task.title}`}>
      <CardBody task={task} users={users} badgeField={badgeField} />
    </div>
  );
}

function CardBody({ task, users, badgeField, lifted }: { task: Task; users: User[]; badgeField?: FieldDef; lifted?: boolean }) {
  const badge = badgeField ? task.customFields[badgeField.key] : undefined;
  const option = badgeField?.options.find((o) => o.id === badge);
  return (
    <article className={`card edge-${task.priority} ${lifted ? "lifted" : ""}`}>
      <div className="card-top">
        <p className="card-title">{task.title}</p>
        <span className="avatars">
          {task.assigneeIds.map((id) => (
            <span key={id} className="avatar" style={avatarStyle(id)} title={userName(users, id)}>{initials(userName(users, id))}</span>
          ))}
        </span>
      </div>
      <div className="card-meta">
        {task.priority !== "none" && <span className={`prio prio-${task.priority}`}>{priorityLabel[task.priority]}</span>}
        {badgeField && badge !== undefined && (
          <span className="badge" style={option?.color ? { ["--dot" as string]: option.color } : undefined}>
            {formatFieldValue(badgeField, badge)}
          </span>
        )}
        {task.dueDate && <span className={`due ${isOverdue(task.dueDate) ? "overdue" : ""}`}>{shortDate(task.dueDate)}</span>}
      </div>
    </article>
  );
}
