# Cadence — common tasks. Run `make` or `make help` for the list.

.DEFAULT_GOAL := help
.PHONY: help dev db-up db-down db-reset db-logs build test test-db web-build fmt

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

dev: ## Run everything locally (persistent Postgres + API + web)
	./dev.sh

db-up: ## Start the persistent Postgres (data kept in a Docker volume)
	docker compose up -d db

db-down: ## Stop Postgres — DATA IS KEPT (volume preserved)
	docker compose down

db-reset: ## Stop Postgres and DELETE ALL DATA (drops the volume)
	docker compose down -v

db-logs: ## Tail Postgres logs
	docker compose logs -f db

build: ## Build the Go API server
	cd server && go build ./...

test: ## Run Go tests (in-memory suites only)
	cd server && go test ./...

test-db: ## Run Go tests incl. the Postgres RLS isolation suite (needs `make db-up`)
	cd server && TEST_DATABASE_URL="postgres://cadence:cadence@localhost:$${CADENCE_DB_PORT:-55432}/cadence?sslmode=disable" go test ./...

web-build: ## Production web build
	npm ci && npm run build

fmt: ## Format Go code
	cd server && gofmt -w .
