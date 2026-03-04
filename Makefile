.DEFAULT_GOAL := check
BACKEND_DIRS := backends/mysql backends/sqlite backends/pgsql backends/picodata
CORE_PKGS := ./outbox/... ./shared/... ./tools/...
CORE_GO_FILES := $(shell find outbox shared tools -type f -name '*.go')
BACKEND_GO_FILES := $(shell find backends -type f -name '*.go')
CORE_VERSION ?= v0.9.0

check: generate fmt vet lint test-core test-backends test-race-core cover-html
check-all: devup check test-integration-all devdown

generate:
	go generate $(CORE_PKGS)
	@for d in $(BACKEND_DIRS); do (cd $$d && go generate ./...); done

fmt: fmt-core fmt-backends

fmt-core:
	go fmt ./...
	gofumpt -l -w $(CORE_GO_FILES)
	gci write -s standard -s default -s "prefix(github.com/assurrussa/outbox)" outbox shared tools

fmt-backends:
	gofumpt -l -w $(BACKEND_GO_FILES)

lint: lint-core

lint-core:
	golangci-lint run -v --fix --timeout=5m $(CORE_PKGS)

vet: vet-core vet-backends

vet-core:
	go vet ./...

vet-backends:
	@for d in $(BACKEND_DIRS); do (cd $$d && go vet ./...); done

test: test-core test-backends

test-core:
	go test ./...

test-backends:
	@for d in $(BACKEND_DIRS); do (cd $$d && go test ./...); done

test-race-core:
	go test -race -count=5 ./...

test-integration: test-integration-all

test-integration-all: test-integration-mysql test-integration-sqlite test-integration-pgsql test-integration-picodata

test-integration-mysql:
	cd backends/mysql && go test -tags integration -race ./...

test-integration-sqlite:
	cd backends/sqlite && go test -tags integration -race ./...

test-integration-pgsql:
	cd backends/pgsql && go test -tags integration -race ./...

test-integration-picodata:
	cd backends/picodata && go test -tags integration -race ./...

release-ready-backends:
	@for d in $(BACKEND_DIRS); do \
		echo "==> $$d use core $(CORE_VERSION)"; \
		(cd $$d && \
			go mod edit -require=github.com/assurrussa/outbox@$(CORE_VERSION)); \
	done

release-verify-backends:
	@for d in $(BACKEND_DIRS); do \
		echo "==> verify $$d (GOWORK=off)"; \
		(cd $$d && \
			GOWORK=off go mod tidy && \
			GOWORK=off go test ./...); \
	done

refresh-backends:
	@echo "==> refresh go.work"
	@(go mod tidy)
	@echo "==> refresh examples/base-app"
	@(cd examples/base-app && \
		go mod tidy)
	@echo "==> refresh examples/base-app-pgsql"
	@(cd examples/base-app-pgsql && \
		go get github.com/assurrussa/outbox/backends/pgsql && \
		go mod tidy)
	@echo "==> refresh examples/base-app-mysql"
	@(cd examples/base-app-mysql && \
		go get github.com/assurrussa/outbox/backends/mysql && \
		go mod tidy)
	@echo "==> refresh examples/base-app-sqlite"
	@(cd examples/base-app-sqlite && \
		go get github.com/assurrussa/outbox/backends/sqlite && \
		go mod tidy)
	@echo "==> refresh examples/base-app-picodata"
	@(cd examples/base-app-picodata && \
		go get github.com/assurrussa/outbox/backends/picodata && \
		go mod tidy)

cover-html:
	@go test -coverprofile=./coverage.text -covermode=atomic $(shell go list ./...)
	@go tool cover -html=./coverage.text -o ./cover.html && rm ./coverage.text

bench-all:
	go test -bench=. -benchmem ./...
	@for d in $(BACKEND_DIRS); do (cd $$d && go test -bench=. -benchmem ./...); done

devup:
	docker compose --profile mysql --profile pgsql --profile picodata up -d

devdown:
	docker compose --profile mysql --profile pgsql --profile picodata down --remove-orphans
