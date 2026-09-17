# Cadence — common tasks. Run `make` or `make help` for the list.

.DEFAULT_GOAL := help
.PHONY: help dev db-up db-down db-reset db-logs build test test-db web-build web-embed fmt \
        up down logs docker-build invite backup restore

# Local development only: the compose file refuses to start without a DB
# password, so supply the throwaway one here (.env overrides it).
export CADENCE_DB_PASSWORD ?= cadence

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

test-db: ## Run Go tests incl. the Postgres suites (needs `make db-up`; they open the Store as a NOBYPASSRLS role they create)
	cd server && TEST_DATABASE_URL="postgres://cadence:$${CADENCE_DB_PASSWORD}@localhost:$${CADENCE_DB_PORT:-55432}/cadence?sslmode=disable" go test ./...

web-build: ## Production web build (./dist, for a separate static host)
	npm ci && npm run build

web-embed: ## Build the web app INTO the server (server/internal/web/dist) so one binary serves both
	VITE_API_URL="" npm run build -- --outDir server/internal/web/dist --emptyOutDir
	@touch server/internal/web/dist/.gitkeep
	cd server && go build -o ./bin/cadence-server ./cmd/cadence-server
	@echo "built server/bin/cadence-server with the web app embedded"

fmt: ## Format Go code
	cd server && gofmt -w .

# ---- private deployment (docker compose) -------------------------------------

docker-build: ## Build the cadence image (web + api, distroless)
	docker compose build app

up: ## Start the private deployment (db + app) from .env
	docker compose up -d --build

down: ## Stop the private deployment — DATA IS KEPT
	docker compose down

logs: ## Tail app + db logs
	docker compose logs -f

invite: ## Pre-create an account for CADENCE_SIGNUP=invite:  make invite EMAIL=you@example.com
	@test -n "$(EMAIL)" || { echo "usage: make invite EMAIL=you@example.com"; exit 2; }
	docker compose exec app /cadence-server invite "$(EMAIL)"

backup: ## pg_dump into ./backups (keeps the newest CADENCE_BACKUP_KEEP, default 14)
	scripts/backup.sh

restore: ## Rehearse a restore into a fresh DB:  make restore DUMP=backups/cadence-….dump
	@test -n "$(DUMP)" || { echo "usage: make restore DUMP=backups/cadence-<stamp>.dump"; exit 2; }
	scripts/restore.sh "$(DUMP)"
