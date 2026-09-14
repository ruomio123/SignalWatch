ENV_FILE := .env
COMPOSE_FILE := deploy/compose.yaml
MIGRATIONS_DIR := migrations
GOOSE ?= goose
COMPOSE := docker compose --env-file $(ENV_FILE) -f $(COMPOSE_FILE)

-include $(ENV_FILE)

export AI_SUBSCRIPTION_AGENT_DAILY_LIMIT AI_PAPER_QA_DAILY_LIMIT
export AI_CONFIG_TEST_MIN_INTERVAL AI_GENERATION_MIN_INTERVAL AI_CONFIG_TEST_DAILY_LIMIT AI_PAPER_DAILY_LIMIT AI_DIGEST_DAILY_LIMIT
export AI_ENABLED AI_CREDENTIAL_KEYS AI_CREDENTIAL_ACTIVE_KEY_VERSION AI_ENABLED_PROVIDERS AI_WORKERS AI_QUEUE_CAPACITY
export APP_ENV APP_PUBLIC_URL HTTP_ADDR LOG_LEVEL WORKER_HEARTBEAT
export MYSQL_DATABASE MYSQL_USER MYSQL_PASSWORD MYSQL_ROOT_PASSWORD
export MYSQL_DSN MYSQL_MAX_OPEN_CONNS MYSQL_MAX_IDLE_CONNS
export REDIS_ADDR REDIS_PASSWORD REDIS_DB
export JWT_SECRET JWT_TTL JWT_ISSUER AUTH_SESSION_TTL
export COLLECTOR_LOCK_TTL ARXIV_BOOTSTRAP_LOOKBACK ARXIV_RECOVERY_OVERLAP
export ARXIV_DAILY_SYNC_TIME ARXIV_SYNC_RETRY_INTERVAL ARXIV_FEED_ENDPOINT
export ARXIV_PAGE_SIZE ARXIV_MAX_PAGES ARXIV_MAX_RESPONSE_BYTES
export ARXIV_REQUEST_ATTEMPTS ARXIV_REQUEST_BACKOFF ARXIV_REQUEST_INTERVAL
export ARXIV_HTTP_TIMEOUT
export MATCHER_WORKERS MATCHER_QUEUE_CAPACITY
export OPS_STATUS_RETENTION
export DIGEST_INTERVAL
export MAIL_WORKERS MAIL_QUEUE_CAPACITY
export SMTP_ADDR SMTP_FROM SMTP_USERNAME SMTP_PASSWORD SMTP_STARTTLS SMTP_TIMEOUT

# Keep build caches and temporary files under this project, out of shared /tmp.
export GOCACHE ?= $(CURDIR)/.cache/go-build
export GOTMPDIR ?= $(CURDIR)/.cache/tmp
export TMPDIR ?= $(CURDIR)/.cache/tmp
export npm_config_cache ?= $(CURDIR)/.cache/npm
export NODE_COMPILE_CACHE ?= $(CURDIR)/.cache/node-compile

.PHONY: require-env prepare-cache require-node deps-up deps-down api worker backend-build migrate-up migrate-down migrate-status frontend web-dev web-build web-publish web-rollback

require-env:
	@test -f "$(ENV_FILE)" || { echo "error: .env not found; run: cp .env.example .env" >&2; exit 1; }

prepare-cache:
	@mkdir -p "$(CURDIR)/.cache"
	@chmod 700 "$(CURDIR)/.cache"
	@mkdir -p "$$GOCACHE" "$$GOTMPDIR" "$$TMPDIR" "$$npm_config_cache" "$$NODE_COMPILE_CACHE"

deps-up: require-env
	$(COMPOSE) up -d --wait

deps-down: require-env
	$(COMPOSE) down

migrate-up: require-env
	@$(GOOSE) -dir "$(MIGRATIONS_DIR)" mysql "$$MYSQL_DSN" up

migrate-down: require-env
	@$(GOOSE) -dir "$(MIGRATIONS_DIR)" mysql "$$MYSQL_DSN" down

migrate-status: require-env
	@$(GOOSE) -dir "$(MIGRATIONS_DIR)" mysql "$$MYSQL_DSN" status

api: require-env prepare-cache
	@exec go run ./cmd/api

worker: require-env prepare-cache
	@exec go run ./cmd/worker

backend-build: prepare-cache
	@mkdir -p bin
	go build -trimpath -o bin/ ./cmd/api ./cmd/worker ./cmd/ops

require-node:
	@node web/scripts/check-node.cjs

web-dev: require-node prepare-cache
	@cd web && exec npm run dev

web-build: require-node prepare-cache
	npm --prefix web ci
	npm --prefix web run build

frontend: web-build

WEB_ROOT ?= $(CURDIR)/.deploy/web
WEB_CHECK_URL ?= https://localhost
export RELEASE WEB_ROOT WEB_CHECK_URL WEB_CA_FILE

web-publish:
	@python3 scripts/web-release.py publish

web-rollback:
	@python3 scripts/web-release.py rollback
