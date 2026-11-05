SHELL := /bin/sh
GO ?= go
# Keep Go modules enabled when the checkout itself is inside GOPATH.
# 即使仓库位于 GOPATH 内，也使用外部 GOPATH 并启用 Go Modules。
export GOPATH := /tmp/paios-gopath
export GO111MODULE := on

DOCKER_COMPOSE_TEST := docker compose -p paios-dbtest --env-file .env.example --profile test -f deployments/compose/docker-compose.yml
PAIOS_TEST_DATABASE_URL ?= postgres://paios_test:paios-test-only@127.0.0.1:55432/paios_test?sslmode=disable

.PHONY: build build-arm64 test vet run migrate docker-up docker-down db-test-up db-test-down db-test-clean test-integration test-autonomous-e2e test-source-live fmt

build:
	# Build the HTTP service binary.
	# 编译 HTTP 服务二进制文件。
	mkdir -p bin
	$(GO) build -o bin/paios ./cmd/paios

build-arm64:
	# Cross-build the Go Core for Apple Silicon macOS.
	# 为 Apple Silicon macOS 交叉编译 Go Core。
	mkdir -p bin
	GOOS=darwin GOARCH=arm64 $(GO) build -o bin/paios-darwin-arm64 ./cmd/paios

test:
	# Run all package unit and handler tests; opt-in DB integration tests need their own URL.
	# 运行各包单元及 Handler 测试；数据库集成测试需单独配置测试数据库 URL。
	$(GO) test ./...

vet:
	# Run Go's static analyzer over all packages.
	# 对所有 Go 包运行静态分析器。
	$(GO) vet ./...

fmt:
	# Format all Go source files without changing generated/runtime data.
	# 格式化所有 Go 源文件，不修改生成数据或运行时数据。
	gofmt -w $$(find cmd internal migrations pkg -name '*.go' -type f)

run:
	# Load the optional local .env file and start the service.
	# 加载可选的本地 .env 文件并启动服务。
	@set -a; [ ! -f .env ] || . ./.env; set +a; $(GO) run ./cmd/paios

migrate:
	# Load the optional developer environment file, then apply SQL migrations.
	# 加载可选开发环境文件，然后应用 SQL 数据库迁移。
	@set -a; [ ! -f .env ] || . ./.env; set +a; $(GO) run ./cmd/paios migrate

docker-up:
	# Start the PostgreSQL infrastructure required by the native Go Core.
	# 启动原生 Go Core 所需的 PostgreSQL 基础设施。
	@test -f .env || (echo "Create .env from .env.example first"; exit 1)
	docker compose --env-file .env -f deployments/compose/docker-compose.yml up -d

docker-down:
	# Stop the project's configured Compose services; this is not used by test cleanup.
	# 停止项目配置的 Compose 服务；测试清理不会调用此目标。
	docker compose --env-file .env -f deployments/compose/docker-compose.yml down

# Start/remove only the isolated, named-volume integration database service.
# 只启动/移除使用专用命名数据卷的隔离集成测试数据库服务。
db-test-up:
	$(DOCKER_COMPOSE_TEST) up -d --wait postgres-test

db-test-down:
	$(DOCKER_COMPOSE_TEST) rm --stop --force postgres-test

# Remove only the explicitly named disposable integration-test volume.
# 只删除明确命名的一次性集成测试数据卷。
db-test-clean:
	$(DOCKER_COMPOSE_TEST) rm --stop --force postgres-test
	@docker volume rm -f paios-integration-test-data 2>/dev/null || true

# Always clean only the isolated test container and volume; never run Compose down here.
# 结束后只清理隔离的测试容器和数据卷；这里绝不执行会影响其他服务的 Compose down。
test-integration: build
	@set -eu; \
	$(MAKE) db-test-clean; \
	trap 'test_exit=$$?; $(DOCKER_COMPOSE_TEST) rm --stop --force postgres-test >/dev/null 2>&1 || true; docker volume rm -f paios-integration-test-data >/dev/null 2>&1 || true; exit $$test_exit' EXIT INT TERM; \
	$(DOCKER_COMPOSE_TEST) up -d --wait postgres-test; \
	PAIOS_TEST_DATABASE_URL='$(PAIOS_TEST_DATABASE_URL)' $(GO) test -v ./tests/integration -run '^TestPostgresMigrationsFromEmptyDatabaseAndIdempotency$$' -count=1; \
	PAIOS_TEST_DATABASE_URL='$(PAIOS_TEST_DATABASE_URL)' $(GO) test ./... -count=1; \
	PAIOS_TEST_DATABASE_URL='$(PAIOS_TEST_DATABASE_URL)' $(GO) test -race ./... -count=1; \
	$(GO) vet ./...; \
	PAIOS_TEST_DATABASE_URL='$(PAIOS_TEST_DATABASE_URL)' $(GO) test -v ./tests/integration -run '^TestDurableWorkerCrashAndCheckpointRecoveryAcceptance$$' -count=1; \
	PAIOS_TEST_DATABASE_URL='$(PAIOS_TEST_DATABASE_URL)' PAIOS_TEST_RESTART_MODE=prepare $(GO) test ./tests/integration -run '^TestPostgresPersistenceAcrossServerRestart$$' -count=1 -v; \
	$(DOCKER_COMPOSE_TEST) restart postgres-test; \
	$(DOCKER_COMPOSE_TEST) up -d --wait postgres-test; \
	PAIOS_TEST_DATABASE_URL='$(PAIOS_TEST_DATABASE_URL)' PAIOS_TEST_RESTART_MODE=verify $(GO) test ./tests/integration -run '^TestPostgresPersistenceAcrossServerRestart$$' -count=1 -v

# Explicit live-network source capture acceptance. This target only uses the disposable integration DB
# and refuses to run unless both a URL and exact allowlisted hostname(s) are supplied.
# 仅在显式授权时进行真实网络来源采集验收；只使用一次性集成数据库，并要求 HTTPS URL 和精确主机白名单。
test-source-live:
	@test -n "$$PAIOS_LIVE_SOURCE_URL" || (echo "BLOCKED: set PAIOS_LIVE_SOURCE_URL to an explicitly selected HTTPS source" >&2; exit 2)
	@test -n "$$PAIOS_LIVE_SOURCE_ALLOWED_HOSTS" || (echo "BLOCKED: set PAIOS_LIVE_SOURCE_ALLOWED_HOSTS to exact public hostnames" >&2; exit 2)
	@set -eu; \
	$(MAKE) db-test-clean; \
	trap 'test_exit=$$?; $(DOCKER_COMPOSE_TEST) rm --stop --force postgres-test >/dev/null 2>&1 || true; docker volume rm -f paios-integration-test-data >/dev/null 2>&1 || true; exit $$test_exit' EXIT INT TERM; \
	$(DOCKER_COMPOSE_TEST) up -d --wait postgres-test; \
	PAIOS_TEST_DATABASE_URL='$(PAIOS_TEST_DATABASE_URL)' PAIOS_LIVE_SOURCE_URL="$$PAIOS_LIVE_SOURCE_URL" PAIOS_LIVE_SOURCE_ALLOWED_HOSTS="$$PAIOS_LIVE_SOURCE_ALLOWED_HOSTS" $(GO) test -v ./tests/integration -run '^TestPostgresLiveSourceCapture$$' -count=1

# Deterministic Goal Job-driven research E2E on isolated PostgreSQL. It uses a
# controlled HTTP fixture; this is not live-source capture or real-model acceptance.
# 在隔离 PostgreSQL 上运行由 Goal Job 驱动的确定性研究 E2E；使用可控 HTTP fixture，不代表真实来源或真实模型验收。
test-autonomous-e2e: build
	@set -eu; \
	$(MAKE) db-test-clean; \
	trap 'test_exit=$$?; $(DOCKER_COMPOSE_TEST) rm --stop --force postgres-test >/dev/null 2>&1 || true; docker volume rm -f paios-integration-test-data >/dev/null 2>&1 || true; exit $$test_exit' EXIT INT TERM; \
	$(DOCKER_COMPOSE_TEST) up -d --wait postgres-test; \
	PAIOS_TEST_DATABASE_URL='$(PAIOS_TEST_DATABASE_URL)' $(GO) test -v ./tests/integration -run '^(TestAutonomousResearchGoalE2E|TestPostgresUnknownExternalEffectWorkerCrashAndCoreRestart)$$' -count=1
