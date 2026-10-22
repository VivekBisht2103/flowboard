# AI usage log

**Tools:** Claude (Anthropic) as a pair-programmer in an agentic coding session with a shell, a local Postgres 16
and headless Chromium; Playwright for screenshots and E2E; the Go and TypeScript compilers and test runners as the
ground truth. Every claim in the README was checked against running code (tests, curl, screenshots).

> Reviewer note: this log is written for the submitter to extend with their own review comments. The sections
> below record what the AI produced, what failed, and what was changed, as it happened.

## Where AI helped most

- **Domain modelling first, code second.** The status merge-by-key rules, the “key unique along any branch” rule for
  custom fields, and the policy table for the hard cases were written down before implementation and then turned
  into pure functions (`statuses.Resolve`, `fields.Apply`) with unit tests.
- **Boilerplate with sharp edges:** keyset pagination with nullable sort keys (`COALESCE(due_date,'infinity')` in both
  the query and the index), fractional positions with in-transaction renumbering, the outbox worker with
  `FOR UPDATE SKIP LOCKED` and savepoints, a stdlib-only HS256 JWT.
- **Docs and diagrams** generated from the code that exists, then cross-checked (route table vs. router via a test).

## Where AI output was wrong and had to be corrected

| What happened | How it was caught | Fix |
|---|---|---|
| Config endpoints validated the request body **before** checking admin rights, so a member got `400` (and learned validation rules) instead of `403`. | Integration test `TestPermissions_MembersCannotChangeConfigButCanEditTasks` failed | Authorise first in `statuses.Create/Patch` and `fields.Create` |
| Tree JSON used `omitempty` on `children`, so leaves had no `children` key and clients had to special-case it. | Test panicked on a nil slice when walking the tree | Always emit `children: []` |
| Kanban card titles were coloured by priority: the card shared the `.prio-*` class used for the priority label. | Screenshot review of the board | Separate `edge-*` class for the card's left border |
| Container pagination contained a dead string-comparison loop. | Code review of generated code | Rewritten as a numeric keyset comparison |
| A first draft of the status-reorder UI called the API through three nested helper functions with dynamic imports. | Code review | Replaced with a single `api.reorderStatuses` call |
| E2E selectors were ambiguous (`getByRole('button', {name:'Backlog'})` matched the sidebar and the breadcrumb). | Playwright strict-mode failure | Scope sidebar locators to `getByRole('tree')` |
| dnd-kit adds its own `role="status"` live region, so tests looking up the toast by role matched two elements. | Vitest / Playwright failures | Target the toast element directly |

## AI-generated tests that had to be fixed

- **`TestMove_RepeatedMidpointInsertsRenumberColumn`** — the first version always inserted *before the first card*.
  `Between(nil, first)` just subtracts 1024, so the gap never shrank and the renumber path was never exercised; the
  test passed while testing nothing. Rewritten to alternate two cards into the same gap 80 times (halving it each
  time, forcing renumbering) and assert strictly increasing positions and the final order.
- **`TestSeed_EveryListBoardIsConsistent`** — originally sent a PATCH with an unknown custom-field key and expected
  400, which only proved unknown keys are rejected. Changed to re-submit each seeded task's own values and expect 200,
  which re-validates types, options and required fields for all 26 seeded tasks.
- **Frontend error-mapping test** — the first draft cleared a number field and asserted on the request body; it never
  checked that the server's `fieldErrors` were shown. Replaced with a test that clears a required select, gets
  `customFields.component REQUIRED` back, and asserts the message renders next to that field.
- **Outbox tests** were added after the happy-path one: rolled-back writes must not emit events, and a failing
  consumer must roll back the other consumers' writes (savepoint) before dead-lettering.

## What was rejected from AI suggestions

- An ORM and a migrations library: the queries that matter (JSONB containment, keyset, window functions) read better
  as SQL, and an embedded migrator with an advisory lock is ~40 lines.
- “Override set replaces parent” for statuses: simpler to implement, but renaming one column would force a list to
  re-declare the whole set and silently stop receiving parent additions.
- Keeping orphaned custom-field values hidden on move: avoids data loss on a round trip, but resurrects stale values
  later. Chose strip + report in `effects.strippedFields`.
- Trusting the JWT `role` claim: roles are always reloaded from the database.

## Verification environment caveat

The Docker images and `docker-compose.yml` were validated with `docker compose config` and by reproducing each
Dockerfile step outside Docker (`go build` with `-mod=readonly`, clean `npm ci && npm run build && npm test`); the
full stack, tests and Playwright suite were run against a local Postgres 16. Run `docker compose up --build` once on
a Docker host before submitting.
