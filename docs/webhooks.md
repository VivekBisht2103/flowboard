# Webhooks and events

Every task mutation writes an event to the `outbox` table in the **same transaction** as the change. The `worker`
process delivers each event to three consumers: the activity projector, a search-indexer stub, and the webhook
publisher. In Docker Compose the webhook URL points at `webhook-sink`, which verifies signatures and logs what it
receives (`make logs`).

## Event types

| Type | When |
|---|---|
| `task.created` | Task created |
| `task.updated` | Title, description, priority, assignees, due date or custom fields changed |
| `task.status_changed` | Status changed within the same list (PATCH or drag) |
| `task.moved` | Task moved to another list |
| `task.deleted` | Task soft-deleted |
| `status.remapped` | A status config change moved tasks to another status (`remapTo`) |
| `field.deleted` | A custom field was deleted and its values stripped |

Pure reorders within a column emit nothing.

## Payload (`schemaVersion: 1`)

```json
{
  "schemaVersion": 1,
  "id": "evt_brt1mvk53qe8z0p9",
  "type": "task.updated",
  "occurredAt": "2026-10-21T09:08:15.991899868Z",
  "workspaceId": "ws_acme",
  "actor": { "id": "bob", "name": "Bob Builder" },
  "task": { "id": "tsk_seed_02", "title": "Rate-limit public API", "listId": "lst_backlog", "statusKey": "todo" },
  "changes": { "priority": { "from": "urgent", "to": "high" } },
  "summary": "Bob Builder updated \"Rate-limit public API\""
}
```

`task.status_changed`:

```json
{
  "schemaVersion": 1,
  "id": "evt_…",
  "type": "task.status_changed",
  "occurredAt": "…",
  "workspaceId": "ws_acme",
  "actor": { "id": "alice", "name": "Alice Admin" },
  "task": { "id": "tsk_seed_10", "title": "Billing page empty state", "listId": "lst_sprint", "statusKey": "qa" },
  "changes": { "statusKey": { "from": "todo", "to": "qa" } },
  "summary": "Alice Admin moved \"Billing page empty state\" from Ready to QA"
}
```

`task.moved` (cross-list, with a mapped status and stripped fields):

```json
{
  "type": "task.moved",
  "task": { "id": "tsk_seed_06", "title": "Dark mode for settings", "listId": "lst_sprint", "statusKey": "blocked" },
  "changes": {
    "listId": { "from": "lst_backlog", "to": "lst_sprint" },
    "statusKey": { "from": "in_review", "to": "blocked" },
    "customFields": { "labels": { "from": ["feature"], "to": null } }
  },
  "summary": "Alice Admin moved \"Dark mode for settings\" from Backlog to Sprint 12 (Blocked)"
}
```

`status.remapped`:

```json
{
  "type": "status.remapped",
  "changes": { "listId": "lst_sprint", "from": "qa", "to": "in_progress", "taskCount": 2 },
  "summary": "Alice Admin remapped 2 task(s) in Sprint 12 from qa to in_progress"
}
```

## Receiver contract

```
POST <WEBHOOK_URL>
Content-Type: application/json
X-Flowboard-Event-Id:   evt_…            (dedupe key)
X-Flowboard-Event-Type: task.updated
X-Flowboard-Signature:  sha256=<hex HMAC-SHA256 of the raw body with WEBHOOK_SECRET>
```

- Respond with any 2xx to acknowledge. Non-2xx or timeouts (5 s) are retried with exponential backoff
  (2, 4, 8 … seconds); after 8 attempts the event is dead-lettered in `outbox` (`dead = true`, `last_error`).
- Delivery is **at least once** and ordering is per-outbox-id, not guaranteed across retries. Dedupe on
  `X-Flowboard-Event-Id`; compare `occurredAt` if you keep your own copy of task state.
- Verify the signature before parsing. Reference implementation: `runWebhookSink` in `backend/cmd/flowboard/main.go`.
- Additive payload fields may appear within `schemaVersion: 1`; removals or meaning changes bump the version.
