# 本地环境变量文件和 Docker Compose 配置文件的位置。
ENV_FILE := .env
COMPOSE_FILE := deploy/compose.yaml
# 数据库迁移文件所在目录。
MIGRATIONS_DIR := migrations

# 允许调用方覆盖 goose 路径，例如：
# make GOOSE=/custom/path/goose migrate-up
GOOSE ?= goose
# 统一 Docker Compose 命令，确保它明确读取项目根目录下的 .env。
COMPOSE := docker compose --env-file $(ENV_FILE) -f $(COMPOSE_FILE)

# 尝试读取本地 .env。
#
# 前面的减号表示：文件不存在时，Make 暂时不直接报错。
# api、worker 等需要环境变量的命令会通过 require-env 给出更明确的提示。
-include $(ENV_FILE)

# 将从 .env 读取的变量导出给 Go 程序和 Docker Compose。
#
# 这里显式列出变量，避免把 Make 自身的所有变量都导出到子进程。
export APP_ENV HTTP_ADDR LOG_LEVEL WORKER_HEARTBEAT
export MYSQL_DATABASE MYSQL_USER MYSQL_PASSWORD MYSQL_ROOT_PASSWORD
export MYSQL_DSN MYSQL_MAX_OPEN_CONNS MYSQL_MAX_IDLE_CONNS
export REDIS_ADDR REDIS_PASSWORD REDIS_DB
export JWT_SECRET JWT_TTL JWT_ISSUER
export COLLECTOR_INTERVAL COLLECTOR_LOCK_TTL ARXIV_LOOKBACK
export ARXIV_PAGE_SIZE ARXIV_MAX_PAGES ARXIV_MAX_RESPONSE_BYTES
export ARXIV_REQUEST_ATTEMPTS ARXIV_REQUEST_BACKOFF ARXIV_REQUEST_INTERVAL
export ARXIV_HTTP_TIMEOUT
export MATCHER_WORKERS MATCHER_QUEUE_CAPACITY
export DIGEST_INTERVAL DIGEST_LOCK_TTL DIGEST_COMPLETION_TTL
export MAIL_WORKERS MAIL_QUEUE_CAPACITY
export SMTP_ADDR SMTP_FROM SMTP_USERNAME SMTP_PASSWORD SMTP_STARTTLS SMTP_TIMEOUT
export M1_TEST_MYSQL_DSN M4_TEST_SMTP_ADDR

# 这些名称代表操作，不代表同名文件。
# 即使目录中出现名为 test、api 的文件，Make 仍然会执行对应命令。
.PHONY: deps-up deps-down api worker fmt vet test test-race openapi-check \
	test-integration m1-verify m2-verify m3-verify m4-verify require-env require-test-dsn migrate-up \
	migrate-down migrate-status migrate-test-up migrate-test-status
# 检查本地环境变量文件是否存在。
# api 和 worker 缺少 .env 时，会在真正启动之前停止并显示处理方法。
require-env:
	@test -f "$(ENV_FILE)" || { \
		echo "error: .env not found; run: cp .env.example .env" >&2; \
		exit 1; \
	}

# 集成验收绝不回退使用开发 DSN；调用方必须显式提供独立测试库。
require-test-dsn:
	@test -n "$(M1_TEST_MYSQL_DSN)" || { \
		echo "error: M1_TEST_MYSQL_DSN is required and its database name must end in _test" >&2; \
		exit 1; \
	}
	@case "$(M1_TEST_MYSQL_DSN)" in \
		*/*_test|*/*_test\?*) ;; \
		*) echo "error: M1_TEST_MYSQL_DSN database name must end in _test" >&2; exit 1 ;; \
	esac

# 启动 MySQL、Redis 和 Mailpit。
deps-up: require-env
	$(COMPOSE) up -d

# 停止并删除 Compose 创建的容器和网络。
# 没有使用 --volumes 或 -v，所以不会删除 MySQL、Redis 数据卷。
deps-down: require-env
	$(COMPOSE) down
# 应用所有尚未执行的数据库迁移。
migrate-up: require-env
	@$(GOOSE) -dir "$(MIGRATIONS_DIR)" mysql "$$MYSQL_DSN" up

# 只回滚最近应用的一个迁移版本。
# 不使用 reset 或 down-to 0，避免一次删除全部业务表。
migrate-down: require-env
	@$(GOOSE) -dir "$(MIGRATIONS_DIR)" mysql "$$MYSQL_DSN" down

# 查看每个迁移版本是已应用还是待执行。
migrate-status: require-env
	@$(GOOSE) -dir "$(MIGRATIONS_DIR)" mysql "$$MYSQL_DSN" status

# 集成数据库必须由调用方预先创建；这里仅应用/查看项目迁移。
migrate-test-up: require-test-dsn
	@$(GOOSE) -dir "$(MIGRATIONS_DIR)" mysql "$$M1_TEST_MYSQL_DSN" up

migrate-test-status: require-test-dsn
	@$(GOOSE) -dir "$(MIGRATIONS_DIR)" mysql "$$M1_TEST_MYSQL_DSN" status
# 加载 .env 中导出的变量并启动 API。
api: require-env
	@exec go run ./cmd/api

# 加载 .env 中导出的变量并启动 Worker。
worker: require-env
	@exec go run ./cmd/worker

# 格式化项目中的所有 Go 包。
fmt:
	go fmt ./...

# 对项目中的所有 Go 包执行静态检查。
vet:
	go vet ./...

# 执行项目中的全部测试。
test:
	go test ./...

# 启用数据竞争检测并执行全部测试。
test-race:
	go test -race ./...

# kin-openapi v0.133.0 在 Go 测试中加载并语义校验 OpenAPI 3.1。
openapi-check:
	go test ./internal/contract

# 测试代码还会校验数据库名以 _test 结尾，避免误用开发数据库。
test-integration: require-test-dsn
	go test -count=1 ./internal/integration

# 递归 make 保证迁移、静态检查和动态验收严格按此顺序执行。
m1-verify: require-test-dsn
	@$(MAKE) migrate-test-up
	@$(MAKE) migrate-test-status
	@$(MAKE) fmt
	@$(MAKE) vet
	@$(MAKE) openapi-check
	@$(MAKE) test
	@$(MAKE) test-race
	@$(MAKE) test-integration

# 当前全量封板检查；保留 m1-verify 以兼容原有开发命令。
m2-verify: m1-verify

# M3 沿用五表结构；全量验收额外由 matcher 单元/竞态/真实 MySQL 测试覆盖。
m3-verify: m2-verify

# M4 沿用五表结构；全量验收额外覆盖 Digest、邮件并发与 Mailpit。
m4-verify: m3-verify
