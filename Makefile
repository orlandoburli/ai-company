# AI Company — development harness.
#
# Two database roles, deliberately: MIGRATE_URL owns the schema, DATABASE_URL is
# the runtime role and cannot create tables or bypass RLS. See README and
# docs/mvp-design.md §10.

DATABASE_URL ?= postgres://aic@localhost/aic
MIGRATE_URL  ?= postgres://aic_owner@localhost/aic
ADDR         ?= :8080
SEED_FLAGS   ?=
WEB_DIR      := web

.PHONY: dev migrate seed api smoke smoke-workflow web web-install test build fmt vet

# Migrate, seed and run API + web together. The seed is idempotent, so this is
# safe to run every time.
dev: migrate seed
	@test -d $(WEB_DIR)/node_modules || { \
	  echo "$(WEB_DIR)/node_modules is missing: run 'make web-install' first"; exit 1; }
	@echo "API on http://localhost$(ADDR)  —  web on http://localhost:3000"
	@trap 'kill 0' INT TERM EXIT; \
	  DATABASE_URL="$(DATABASE_URL)" AIC_ADDR="$(ADDR)" go run ./cmd/server & \
	  (cd $(WEB_DIR) && npm run dev) & \
	  wait

# Schema is applied by the owner role, never by the server.
migrate:
	@DATABASE_URL="$(MIGRATE_URL)" go run ./cmd/migrate

# Development tenant + three products + a realistic backlog. No-op when the
# tenant already exists.
seed:
	@DATABASE_URL="$(DATABASE_URL)" go run ./cmd/seed $(SEED_FLAGS)

api:
	@DATABASE_URL="$(DATABASE_URL)" AIC_ADDR="$(ADDR)" go run ./cmd/server

# End-to-end sanity check against a running, seeded server. Defaults match the
# tenant cmd/seed creates; AIC_URL / AIC_EMAIL / AIC_PASSWORD override them.
smoke:
	@./scripts/smoke.sh

# The M3 path: start a real run, decide its checkpoint, wait for the end.
smoke-workflow:
	@./scripts/smoke-workflow.sh

web-install:
	cd $(WEB_DIR) && npm install

web:
	cd $(WEB_DIR) && npm run dev

# The isolation suite creates and drops databases: point AIC_TEST_DSN at a
# disposable Postgres, never a shared one. Patterns are explicit because
# web/node_modules carries a vendored Go package that must not enter the suite.
test:
	@test -n "$(AIC_TEST_DSN)" || { \
	  echo "set AIC_TEST_DSN to a disposable Postgres (see README)"; exit 1; }
	go test -count=1 ./cmd/... ./internal/...

build:
	go build ./cmd/... ./internal/...

fmt:
	gofmt -l -w cmd internal

vet:
	go vet ./cmd/... ./internal/...
