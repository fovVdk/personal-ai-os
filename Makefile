SHELL := /bin/sh
GO ?= go
# Keep Go modules enabled when the checkout itself is inside GOPATH.
# 即使仓库位于 GOPATH 内，也使用外部 GOPATH 并启用 Go Modules。
export GOPATH := /tmp/paios-gopath
export GO111MODULE := on

.PHONY: build test vet run migrate docker-up docker-down fmt

build:
	# Build the HTTP service binary.
	# 编译 HTTP 服务二进制文件。
	mkdir -p bin
	$(GO) build -o bin/paios ./cmd/paios

test:
	# Run all package unit and handler tests; opt-in DB integration tests need their own URL.
	# 运行各包单元及 Handler 测试；数据库集成测试需单独配置测试数据库 URL。
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w $$(find cmd internal migrations pkg -name '*.go' -type f)

run:
	# Load the optional local .env file and start the service.
	# 加载可选的本地 .env 文件并启动服务。
	@set -a; [ ! -f .env ] || . ./.env; set +a; $(GO) run ./cmd/paios

migrate:
	@set -a; [ ! -f .env ] || . ./.env; set +a; $(GO) run ./cmd/paios migrate

docker-up:
	@test -f .env || (echo "Create .env from .env.example first"; exit 1)
	docker compose --env-file .env -f deployments/compose/docker-compose.yml up -d

docker-down:
	docker compose --env-file .env -f deployments/compose/docker-compose.yml down
