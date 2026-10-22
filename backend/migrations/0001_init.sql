-- Flowboard schema v1
-- All ids are opaque text ids with a type prefix (e.g. lst_..., tsk_...).

CREATE TABLE users (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    email       text NOT NULL UNIQUE,
    role        text NOT NULL CHECK (role IN ('admin', 'member')),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Hierarchy: one adjacency-list table for every container type.
-- workspace (root) -> space -> folder -> list
CREATE TABLE containers (
    id            text PRIMARY KEY,
    workspace_id  text NOT NULL,
    type          text NOT NULL CHECK (type IN ('workspace', 'space', 'folder', 'list')),
    parent_id     text REFERENCES containers(id),
    name          text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    position      double precision NOT NULL DEFAULT 0,
    visibility    text NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'private')),
    archived_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((type = 'workspace') = (parent_id IS NULL))
);
-- Sibling listing / tree load. Partial index skips archived rows.
CREATE INDEX containers_parent_pos_idx ON containers (parent_id, position, id) WHERE archived_at IS NULL;
CREATE INDEX containers_workspace_idx ON containers (workspace_id) WHERE archived_at IS NULL;

-- Permission grants on a container. One row per (container, user).
CREATE TABLE grants (
    resource_id  text NOT NULL REFERENCES containers(id),
    user_id      text NOT NULL REFERENCES users(id),
    mode         text NOT NULL CHECK (mode IN ('allow', 'deny')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (resource_id, user_id)
);
CREATE INDEX grants_user_idx ON grants (user_id);

-- Status definitions, merged by key down the hierarchy.
-- A row either introduces a new key (name/category/color all set) or overrides
-- an inherited key (any subset set), or hides an inherited key (hidden = true).
CREATE TABLE status_defs (
    id          text PRIMARY KEY,
    owner_id    text NOT NULL REFERENCES containers(id),
    key         text NOT NULL,
    name        text,
    category    text CHECK (category IN ('not_started', 'active', 'done', 'closed')),
    color       text,
    position    double precision,
    hidden      boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_id, key)
);

-- Custom field definitions. Keys are unique along any root-to-leaf path
-- (enforced by the service under a config lock), so resolution is a plain union.
CREATE TABLE field_defs (
    id          text PRIMARY KEY,
    owner_id    text NOT NULL REFERENCES containers(id),
    key         text NOT NULL,
    name        text NOT NULL,
    type        text NOT NULL CHECK (type IN ('text', 'number', 'single_select', 'multi_select', 'date', 'checkbox')),
    required    boolean NOT NULL DEFAULT false,
    options     jsonb NOT NULL DEFAULT '[]',
    position    double precision NOT NULL DEFAULT 0,
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX field_defs_owner_key_idx ON field_defs (owner_id, key) WHERE archived_at IS NULL;

CREATE TABLE tasks (
    id             text PRIMARY KEY,
    workspace_id   text NOT NULL,
    list_id        text NOT NULL REFERENCES containers(id),
    title          varchar(500) NOT NULL CHECK (char_length(title) >= 1),
    description    text NOT NULL DEFAULT '',
    status_key     text NOT NULL,
    priority       text NOT NULL DEFAULT 'none' CHECK (priority IN ('urgent', 'high', 'normal', 'low', 'none')),
    priority_rank  smallint GENERATED ALWAYS AS (
                     CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 ELSE 4 END
                   ) STORED,
    assignee_ids   text[] NOT NULL DEFAULT '{}',
    due_date       timestamptz,
    custom_fields  jsonb NOT NULL DEFAULT '{}',
    position       double precision NOT NULL DEFAULT 0,
    version        integer NOT NULL DEFAULT 1,
    created_by     text REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz
);
-- Board load: all live tasks of a list ordered by column then position.
CREATE INDEX tasks_board_idx ON tasks (list_id, status_key, position, id) WHERE deleted_at IS NULL;
-- List-view keyset pagination on the sortable columns.
CREATE INDEX tasks_list_due_idx ON tasks (list_id, (COALESCE(due_date, 'infinity'::timestamptz)), id) WHERE deleted_at IS NULL;
CREATE INDEX tasks_list_priority_idx ON tasks (list_id, priority_rank, id) WHERE deleted_at IS NULL;
CREATE INDEX tasks_list_created_idx ON tasks (list_id, created_at, id) WHERE deleted_at IS NULL;
-- Custom-field equality filters (custom_fields @> '{"k": v}') and assignee filters.
CREATE INDEX tasks_custom_fields_gin ON tasks USING gin (custom_fields jsonb_path_ops) WHERE deleted_at IS NULL;
CREATE INDEX tasks_assignees_gin ON tasks USING gin (assignee_ids) WHERE deleted_at IS NULL;

-- Transactional outbox: written in the same transaction as the domain change,
-- drained by the worker process.
CREATE TABLE outbox (
    id            bigserial PRIMARY KEY,
    event_id      text NOT NULL UNIQUE,
    type          text NOT NULL,
    aggregate_id  text NOT NULL,
    payload       jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    available_at  timestamptz NOT NULL DEFAULT now(),
    attempts      integer NOT NULL DEFAULT 0,
    processed_at  timestamptz,
    dead          boolean NOT NULL DEFAULT false,
    last_error    text
);
CREATE INDEX outbox_pending_idx ON outbox (available_at, id) WHERE processed_at IS NULL;

-- Read model built by the worker (eventually consistent).
CREATE TABLE activity (
    id          bigserial PRIMARY KEY,
    event_id    text NOT NULL UNIQUE,      -- idempotency key: redelivery is a no-op
    type        text NOT NULL,
    task_id     text,
    list_id     text,
    actor_id    text,
    summary     text NOT NULL,
    payload     jsonb NOT NULL,
    created_at  timestamptz NOT NULL
);
CREATE INDEX activity_task_idx ON activity (task_id, id DESC);
CREATE INDEX activity_list_idx ON activity (list_id, id DESC);
