.DEFAULT_GOAL := check
.PHONY: full prepare check check-all generate fmt fmt-check fmt-core fmt-check-core fmt-backends fmt-check-backends lint lint-fix lint-core lint-fix-core vet vet-core vet-backends test test-full test-core test-full-core test-backends test-race-core test-integration test-integration-all test-integration-mysql test-integration-sqlite test-integration-pgsql test-integration-picodata cover-html bench-all release-ready-backends release-verify-backends release-readiness-core release-readiness-pgsql release-readiness-backends release-version-check devup devwait devwait-mysql devwait-pgsql devwait-picodata devdown
BACKEND_DIRS := backends/mysql backends/sqlite backends/pgsql backends/picodata
CORE_PKGS := ./outbox/... ./shared/... ./tools/...
CORE_GO_FILES := $(shell find outbox shared tools -type f -name '*.go')
BACKEND_GO_FILES := $(shell find backends -type f -name '*.go')
CORE_VERSION ?= v0.12.0
TEST_OUTBOXLIB_MYSQL_ADDRESS_LOCAL ?= localhost
TEST_OUTBOXLIB_MYSQL_PORT_LOCAL ?= 33306
TEST_OUTBOXLIB_MYSQL_PASSWORD ?= tests-service
TEST_OUTBOXLIB_PSQL_ADDRESS_LOCAL ?= localhost
TEST_OUTBOXLIB_PSQL_PORT_LOCAL ?= 54335
TEST_OUTBOXLIB_PSQL_USERNAME ?= tests-service
TEST_OUTBOXLIB_PSQL_DATABASENAME ?= tests-db-pgsql
TEST_OUTBOXLIB_PICODATA_ADMIN_PASSWORD ?= passWord!123
TEST_OUTBOXLIB_PICODATA_LISTEN_HTTP ?= 8049
TEST_OUTBOXLIB_PICODATA_DSN ?= postgres://admin:passWord!123@localhost:5049?sslmode=disable
GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/go-mod
GOPATH ?= $(CURDIR)/.cache/go-path
GOLANGCI_LINT_CACHE ?= $(CURDIR)/.cache/golangci-lint
export GOCACHE GOMODCACHE GOPATH GOLANGCI_LINT_CACHE
export TEST_OUTBOXLIB_MYSQL_ADDRESS_LOCAL TEST_OUTBOXLIB_MYSQL_PORT_LOCAL TEST_OUTBOXLIB_MYSQL_PASSWORD
export TEST_OUTBOXLIB_PSQL_ADDRESS_LOCAL TEST_OUTBOXLIB_PSQL_PORT_LOCAL
export TEST_OUTBOXLIB_PSQL_USERNAME TEST_OUTBOXLIB_PSQL_DATABASENAME
export TEST_OUTBOXLIB_PICODATA_ADMIN_PASSWORD TEST_OUTBOXLIB_PICODATA_LISTEN_HTTP TEST_OUTBOXLIB_PICODATA_DSN

release-version-check:
	@test -n "$(CORE_VERSION)" || (echo "CORE_VERSION is required" && exit 2)
	@printf '%s\n' "$(CORE_VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$' || \
		(echo "CORE_VERSION must be an exact semver tag" && exit 2)

release-readiness-core: release-version-check prepare check
	@git diff --exit-code -- . ':!.cache'

release-readiness-pgsql: release-version-check
	@actual=$$(cd backends/pgsql && GOWORK=off go list -m -f '{{.Version}}' github.com/assurrussa/outbox); \
		test "$$actual" = "$(CORE_VERSION)" || \
		(echo "pgsql backend resolves core $$actual, expected $(CORE_VERSION)" && exit 2)
	@cd backends/pgsql && GOWORK=off go mod tidy -diff
	@cd backends/pgsql && GOWORK=off go test ./...

release-readiness-backends: release-version-check
	@for d in $(BACKEND_DIRS); do \
		echo "==> verify $$d uses core $(CORE_VERSION)"; \
		actual=$$(cd $$d && GOWORK=off go list -m -f '{{.Version}}' github.com/assurrussa/outbox); \
		if test "$$actual" != "$(CORE_VERSION)"; then \
			echo "$$d resolves core $$actual, expected $(CORE_VERSION)"; \
			exit 2; \
		fi; \
		(cd $$d && GOWORK=off go mod tidy -diff && GOWORK=off go test ./...) || exit 1; \
	done

full: prepare check

prepare: generate fmt lint-fix

check: fmt-check vet lint test-full
check-all:
	@status=0; \
	$(MAKE) devup || status=$$?; \
	if test $$status -eq 0; then $(MAKE) check test-integration-all || status=$$?; fi; \
	$(MAKE) devdown || { cleanup_status=$$?; test $$status -ne 0 || status=$$cleanup_status; }; \
	exit $$status

generate:
	go generate $(CORE_PKGS)
	@for d in $(BACKEND_DIRS); do (cd $$d && go generate ./...); done

fmt: fmt-core fmt-backends

fmt-check: fmt-check-core fmt-check-backends

fmt-core:
	go fmt ./...
	gofumpt -l -w $(CORE_GO_FILES)
	gci write -s standard -s default -s "prefix(github.com/assurrussa/outbox)" outbox shared tools

fmt-check-core:
	@unformatted="$$(gofumpt -l $(CORE_GO_FILES))"; \
		test -z "$$unformatted" || { printf 'gofumpt changes are required:\n%s\nRun: make prepare\n' "$$unformatted" >&2; exit 1; }
	@import_diff="$$(gci diff -s standard -s default -s "prefix(github.com/assurrussa/outbox)" $(CORE_GO_FILES))"; \
		test -z "$$import_diff" || { printf 'gci changes are required:\n%s\nRun: make prepare\n' "$$import_diff" >&2; exit 1; }

fmt-backends:
	gofumpt -l -w $(BACKEND_GO_FILES)

fmt-check-backends:
	@unformatted="$$(gofumpt -l $(BACKEND_GO_FILES))"; \
		test -z "$$unformatted" || { printf 'gofumpt changes are required:\n%s\nRun: make prepare\n' "$$unformatted" >&2; exit 1; }

lint: lint-core

lint-fix: lint-fix-core

lint-core:
	golangci-lint run -v --timeout=5m $(CORE_PKGS)

lint-fix-core:
	golangci-lint run -v --fix --timeout=5m $(CORE_PKGS)

vet: vet-core vet-backends

vet-core:
	go vet ./...

vet-backends:
	@for d in $(BACKEND_DIRS); do (cd $$d && go vet ./...); done

test: test-core test-backends

test-full: test-full-core test-backends

test-core:
	go test ./...

test-full-core:
	go test -race -cover -covermode=atomic -count=1 ./...

test-backends:
	@for d in $(BACKEND_DIRS); do (cd $$d && go test ./...); done

test-race-core:
	go test -race -count=5 ./...

test-integration: test-integration-all

test-integration-all: test-integration-mysql test-integration-sqlite test-integration-pgsql test-integration-picodata

test-integration-mysql:
	cd backends/mysql && go test -count=1 -tags integration -race ./...

test-integration-sqlite:
	cd backends/sqlite && go test -count=1 -tags integration -race ./...

test-integration-pgsql:
	cd backends/pgsql && go test -count=1 -tags integration -race ./...

test-integration-picodata:
	cd backends/picodata && go test -count=1 -p 1 -tags integration -race ./...

release-ready-backends: release-version-check
	@for d in $(BACKEND_DIRS); do \
		echo "==> $$d use core $(CORE_VERSION)"; \
		(cd $$d && \
			go mod edit -require=github.com/assurrussa/outbox@$(CORE_VERSION) && \
			GOWORK=off go mod tidy) || exit 1; \
	done

release-verify-backends:
	@for d in $(BACKEND_DIRS); do \
		echo "==> verify $$d (GOWORK=off)"; \
		(cd $$d && \
			GOWORK=off go mod tidy -diff && \
			GOWORK=off go test ./...) || exit 1; \
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
	@$(MAKE) devwait

devwait: devwait-mysql devwait-pgsql devwait-picodata

devwait-mysql:
	@echo "==> wait for MySQL"
	@for i in $$(seq 1 60); do \
		docker compose exec -T integration-mysql-tests \
			mysqladmin ping -h 127.0.0.1 -uroot -p"$(TEST_OUTBOXLIB_MYSQL_PASSWORD)" --silent \
			>/dev/null 2>&1 && break; \
		test $$i -lt 60 || (echo "MySQL did not become ready" && exit 1); \
		sleep 1; \
	done

devwait-pgsql:
	@echo "==> wait for PostgreSQL"
	@for i in $$(seq 1 60); do \
		docker compose exec -T integration-postgres-tests \
			pg_isready -U "$(TEST_OUTBOXLIB_PSQL_USERNAME)" -d "$(TEST_OUTBOXLIB_PSQL_DATABASENAME)" \
			>/dev/null 2>&1 && break; \
		test $$i -lt 60 || (echo "PostgreSQL did not become ready" && exit 1); \
		sleep 1; \
	done

devwait-picodata:
	@echo "==> wait for Picodata"
	@for i in $$(seq 1 60); do \
		curl -fsS http://127.0.0.1:$(TEST_OUTBOXLIB_PICODATA_LISTEN_HTTP)/ >/dev/null 2>&1 && break; \
		test $$i -lt 60 || (echo "Picodata did not become ready" && exit 1); \
		sleep 1; \
	done

devdown:
	docker compose --profile mysql --profile pgsql --profile picodata down --remove-orphans
