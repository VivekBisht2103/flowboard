.PHONY: up down reset seed test test-api test-web e2e logs cli psql

up:            ## build and start the whole stack
	docker compose up --build

down:          ## stop and remove containers (keeps the database volume)
	docker compose down

reset:         ## stop and delete the database volume
	docker compose down -v

seed:          ## wipe and re-seed demo data in the running stack
	docker compose run --rm seed seed --reset

test: test-api test-web   ## backend + frontend tests, all in Docker

test-api:      ## Go unit + integration tests against the compose Postgres
	docker compose --profile test run --rm --build api-test

test-web:      ## TypeScript typecheck + Vitest component tests
	docker compose --profile test run --rm --build web-test

e2e: seed      ## Playwright against the running stack (start it first with `make up`)
	docker compose --profile e2e run --rm --build e2e

logs:
	docker compose logs -f api worker webhook-sink

cli:           ## e.g. make cli ARGS="board lst_sprint" AS=rahul
	docker compose exec -e FLOWBOARD_URL=http://localhost:8080 -e FLOWBOARD_USER=$(or $(AS),priya) api fbctl $(ARGS)

psql:
	docker compose exec db psql -U flowboard flowboard
