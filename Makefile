# plinth task runner. Targets not listed here are still planned (see README.md).

PG_CONTAINER := plinth-pg
PG_PORT      := 5435

.PHONY: test lint db-up db-down

test: ## unit tests, and the conformance suites against the in-memory stores
	go test ./...

lint: ## go vet and gofmt
	go vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

db-up: ## Postgres 18 in Docker on localhost:5435, beside a product's on 5434
	docker run -d --rm --name $(PG_CONTAINER) -e POSTGRES_PASSWORD=plinth -e POSTGRES_DB=plinth -p $(PG_PORT):5432 postgres:18-alpine
	@until docker exec $(PG_CONTAINER) pg_isready -U postgres -d plinth >/dev/null 2>&1; do sleep 0.5; done
	@echo "postgres://postgres:plinth@localhost:$(PG_PORT)/plinth?sslmode=disable"

db-down: ## stop and remove the test database
	docker rm -f $(PG_CONTAINER)

TEST_DB_URL := postgres://postgres:plinth@localhost:$(PG_PORT)/plinth?sslmode=disable

.PHONY: test-db
test-db: ## the conformance suites, migrations and role checks against Postgres (needs make db-up)
	PLINTH_TEST_DATABASE_URL=$(TEST_DB_URL) go test -count=1 ./...
