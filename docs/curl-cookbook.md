# curl cookbook (second channel)

Everything the web UI does goes through these same `/v1` endpoints. Run against `docker compose up`:

```bash
API=http://localhost:8080/v1
H='Content-Type: application/json'
```

## Auth

```bash
# Dev identity header (mock users: priya | rahul | ananya)
curl -s $API/me -H 'X-User-Id: rahul'

# Or a short-lived JWT (15 min). The role inside is informational; the server reloads it from the DB.
TOKEN=$(curl -s -X POST $API/auth/token -H "$H" -d '{"userId":"ananya"}' | jq -r .token)
curl -s $API/me -H "Authorization: Bearer $TOKEN"

curl -s $API/tree                       # 401 UNAUTHENTICATED
```

## Hierarchy and permissions

```bash
curl -s $API/tree -H 'X-User-Id: rahul'   | jq '.. | objects | select(.type=="list") | .name'
curl -s $API/tree -H 'X-User-Id: ananya' | jq '.. | objects | select(.type=="list") | .name'
curl -s $API/tasks/tsk_seed_20 -H 'X-User-Id: rahul'      # 403 FORBIDDEN (private Security Audit)

# Admin: create a list, reorder it first, grant Rahul access to the private list
curl -s -X POST $API/containers -H 'X-User-Id: priya' -H "$H" -d '{"parentId":"fld_q2","type":"list","name":"Icebox"}'
curl -s -X POST $API/containers/<newListId>/move -H 'X-User-Id: priya' -H "$H" -d '{"beforeId":"lst_backlog"}'
curl -s -X PUT  $API/containers/lst_security/grants/rahul -H 'X-User-Id: priya' -H "$H" -d '{"mode":"allow"}'
```

## Scoped statuses

```bash
curl -s $API/lists/lst_backlog/resolved-statuses -H 'X-User-Id: priya' | jq '[.data[] | .name]'
curl -s $API/lists/lst_sprint/resolved-statuses  -H 'X-User-Id: priya' | jq '[.data[] | .name]'

# Local / inherited / resolved view of a container's config
curl -s $API/containers/lst_sprint/statuses -H 'X-User-Id: priya' | jq '.local'

# Override an inherited status at the folder: affects Backlog, Sprint 12 and Security Audit
curl -s -X PATCH $API/containers/fld_q2/statuses/done -H 'X-User-Id: priya' -H "$H" -d '{"name":"Shipped"}'

# Delete a status that tasks use -> 409 STATUS_IN_USE with counts; then remap atomically
curl -s -X DELETE "$API/containers/lst_sprint/statuses/qa" -H 'X-User-Id: priya'
curl -s -X DELETE "$API/containers/lst_sprint/statuses/qa?remapTo=in_progress" -H 'X-User-Id: priya'

# Column order at a level
curl -s -X PUT $API/containers/lst_backlog/statuses/order -H 'X-User-Id: priya' -H "$H" \
  -d '{"keys":["todo","in_review","in_progress","done"]}'
```

## Custom fields

```bash
curl -s $API/lists/lst_backlog/resolved-fields -H 'X-User-Id: priya' | jq '[.data[] | {key, type, required, ownerType}]'

# Add a field to a folder; redefining an ancestor's key is rejected (409 FIELD_KEY_CONFLICT)
curl -s -X POST $API/containers/fld_q2/fields -H 'X-User-Id: priya' -H "$H" \
  -d '{"key":"risk","name":"Risk","type":"single_select","options":[{"id":"low","label":"Low"},{"id":"high","label":"High"}]}'
curl -s -X POST $API/containers/lst_sprint/fields -H 'X-User-Id: priya' -H "$H" -d '{"key":"component","name":"C","type":"text"}'

# Make an optional field required: needs a backfill value if tasks lack it
curl -s -X PATCH $API/containers/spc_eng/fields/story_points -H 'X-User-Id: priya' -H "$H" -d '{"required":true,"defaultValue":1}'
```

## Tasks

```bash
# Create (component is required on Engineering lists). Wrong type -> 400 with the field path.
curl -s -X POST $API/lists/lst_sprint/tasks -H 'X-User-Id: rahul' -H "$H" \
  -d '{"title":"From curl","customFields":{"component":"backend","story_points":"3"}}'
# {"error":{"code":"VALIDATION_FAILED","message":"customFields.story_points: expected a number", ...}}

curl -s -X POST $API/lists/lst_sprint/tasks -H 'X-User-Id: rahul' -H "$H" \
  -d '{"title":"From curl","priority":"high","dueDate":"2026-11-02","customFields":{"component":"backend","story_points":3}}'

# Merge-patch; null clears; version guards against lost updates (409 VERSION_CONFLICT)
curl -s -X PATCH $API/tasks/tsk_seed_10 -H 'X-User-Id: rahul' -H "$H" -d '{"statusKey":"qa","customFields":{"story_points":5},"version":1}'

# Drag card: column + position
curl -s -X POST $API/tasks/tsk_seed_11/move -H 'X-User-Id: rahul' -H "$H" -d '{"statusKey":"in_progress","afterTaskId":"tsk_seed_14"}'

# Cross-list move: status mapping + stripped fields are reported in "effects"
curl -s -X POST $API/tasks/tsk_seed_06/move -H 'X-User-Id: priya' -H "$H" -d '{"listId":"lst_sprint"}' | jq .effects
curl -s -X POST $API/tasks/tsk_seed_06/move -H 'X-User-Id: priya' -H "$H" -d '{"listId":"lst_sprint","statusPolicy":"reject"}'
# Into Marketing: required "channel" must be supplied
curl -s -X POST $API/tasks/tsk_seed_10/move -H 'X-User-Id: priya' -H "$H" -d '{"listId":"lst_content","customFields":{"channel":"blog"}}'
```

## Reads: board, list view, pagination, filters

```bash
curl -s $API/lists/lst_sprint/board -H 'X-User-Id: rahul' | jq '[.columns[] | {status: .status.name, count}]'

# Keyset pagination: follow page.nextCursor until null
curl -s "$API/lists/lst_backlog/tasks?sort=dueDate&limit=3" -H 'X-User-Id: priya' | jq '.page'
curl -s "$API/lists/lst_backlog/tasks?sort=dueDate&limit=3&cursor=<nextCursor>" -H 'X-User-Id: priya'

# Sort by priority (urgent first), filter by custom field values
curl -s "$API/lists/lst_sprint/tasks?sort=priority" -H 'X-User-Id: priya' | jq '[.data[] | .priority]'
curl -s "$API/lists/lst_backlog/tasks?cf.labels=bug&cf.component=frontend" -H 'X-User-Id: priya' | jq '[.data[] | .title]'

# Activity (eventually consistent; built by the worker)
curl -s $API/tasks/tsk_seed_10/activity -H 'X-User-Id: rahul' | jq '[.data[] | .summary]'
```

## CLI

The same flows from the `fbctl` binary shipped in the backend image:

```bash
make cli ARGS="tree" AS=ananya
make cli ARGS="board lst_sprint" AS=rahul
make cli ARGS="tasks lst_backlog --sort dueDate --cf component=backend"
make cli ARGS="create lst_sprint 'Ship it' component=backend story_points=3" AS=rahul
make cli ARGS="move tsk_seed_06 lst_sprint --policy reject"
```
