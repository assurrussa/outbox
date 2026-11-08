GO_SHARED_CACHE_ROOT ?= $(HOME)/dev/projects/.cache/go
GOCACHE ?= $(GO_SHARED_CACHE_ROOT)/build
GOMODCACHE ?= $(GO_SHARED_CACHE_ROOT)/mod
GOLANGCI_LINT_CACHE ?= $(GO_SHARED_CACHE_ROOT)/lint
GOPATH ?= $(shell GOTOOLCHAIN=local go env GOPATH)

.DEFAULT_GOAL := check
.PHONY: full prepare check check-all generate fmt fmt-check fmt-core fmt-check-core fmt-backends fmt-check-backends lint lint-fix lint-core lint-fix-core lint-backends lint-fix-backends vet vet-core vet-backends test test-full test-core test-full-core test-backends test-backends-standalone test-examples test-race-core test-integration test-integration-all test-integration-mysql test-integration-sqlite test-integration-pgsql test-integration-picodata cover-html bench-all release-ready-backends release-verify-backends release-readiness-core release-readiness-pgsql release-readiness-backends release-version-check refresh-backends devup devwait devwait-mysql devwait-pgsql devwait-picodata devdown
BACKEND_DIRS := backends/mysql backends/sqlite backends/pgsql backends/picodata
EXAMPLE_DIRS := examples/base-app examples/base-app-mysql examples/base-app-sqlite examples/base-app-pgsql examples/base-app-picodata
CORE_PKGS := ./outbox/... ./shared/... ./tools/...
CORE_GO_FILES := $(shell find outbox shared tools -type f -name '*.go')
BACKEND_GO_FILES := $(shell find backends -type f -name '*.go')
CORE_VERSION ?=
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
export GOCACHE GOMODCACHE GOPATH GOLANGCI_LINT_CACHE
export TEST_OUTBOXLIB_MYSQL_ADDRESS_LOCAL TEST_OUTBOXLIB_MYSQL_PORT_LOCAL TEST_OUTBOXLIB_MYSQL_PASSWORD
export TEST_OUTBOXLIB_PSQL_ADDRESS_LOCAL TEST_OUTBOXLIB_PSQL_PORT_LOCAL
export TEST_OUTBOXLIB_PSQL_USERNAME TEST_OUTBOXLIB_PSQL_DATABASENAME
export TEST_OUTBOXLIB_PICODATA_ADMIN_PASSWORD TEST_OUTBOXLIB_PICODATA_LISTEN_HTTP TEST_OUTBOXLIB_PICODATA_DSN

release-version-check:
	@test -n "$(CORE_VERSION)" || (echo "CORE_VERSION is required (e.g. make release-ready-backends CORE_VERSION=v0.13.1)" && exit 2)
	@printf '%s\n' "$(CORE_VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$' || \
		(echo "CORE_VERSION must be an exact semver tag" && exit 2)

release-readiness-core: release-version-check prepare check
	@git diff --exit-code -- . ':!.cache'

release-readiness-pgsql: release-version-check
	@actual=$$(cd backends/pgsql && GOWORK=off go list -m -f '{{.Version}}{{if .Replace}} (replaced:{{.Replace.Path}}){{end}}' github.com/assurrussa/outbox); \
		test "$$actual" = "$(CORE_VERSION)" || \
		(echo "pgsql backend resolves core $$actual, expected $(CORE_VERSION)" && exit 2)
	@cd backends/pgsql && GOWORK=off go mod tidy -diff
	@cd backends/pgsql && GOWORK=off go test ./...

release-readiness-backends: release-version-check
	@for d in $(BACKEND_DIRS); do \
		echo "==> verify $$d uses core $(CORE_VERSION)"; \
		actual=$$(cd $$d && GOWORK=off go list -m -f '{{.Version}}{{if .Replace}} (replaced:{{.Replace.Path}}){{end}}' github.com/assurrussa/outbox); \
		if test "$$actual" != "$(CORE_VERSION)"; then \
			echo "$$d resolves core $$actual, expected $(CORE_VERSION)"; \
			exit 2; \
		fi; \
		(cd $$d && GOWORK=off go mod tidy -diff && GOWORK=off go test ./...) || exit 1; \
	done
	@repo_root="$$(pwd)"; \
	for d in $(EXAMPLE_DIRS); do \
		echo "==> verify $$d uses core $(CORE_VERSION)"; \
		( \
			cd $$d && \
			GOWORK=off go mod tidy -diff && \
			tmp_mod=".go.release-check.mod" && \
			tmp_sum=".go.release-check.sum" && \
			trap 'rm -f "$$tmp_mod" "$$tmp_sum"' EXIT INT TERM && \
			cp go.mod "$$tmp_mod" && \
			{ test ! -f go.sum || cp go.sum "$$tmp_sum"; } && \
			GOWORK=off go mod edit -dropreplace=github.com/assurrussa/outbox "$$tmp_mod" && \
			actual=$$(GOWORK=off go list -mod=mod -modfile="$$tmp_mod" -m -f '{{.Version}}{{if .Replace}} (replaced:{{.Replace.Path}}){{end}}' github.com/assurrussa/outbox) && \
			if test "$$actual" != "$(CORE_VERSION)"; then \
				echo "$$d resolves core $$actual, expected $(CORE_VERSION)"; \
				exit 2; \
			fi && \
			mod_dir=$$(GOWORK=off go list -mod=mod -modfile="$$tmp_mod" -m -f '{{.Dir}}' github.com/assurrussa/outbox) && \
			if test -z "$$mod_dir" || test "$$mod_dir" = "$$repo_root"; then \
				echo "$$d resolved core directory '$$mod_dir' instead of published module cache"; \
				exit 2; \
			fi && \
			GOWORK=off go build -mod=mod -modfile="$$tmp_mod" ./... \
		) || exit $$?; \
	done

full: prepare check

prepare: generate fmt lint-fix

check: fmt-check vet lint test-full-core test-backends-standalone test-examples
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
		status=$$?; \
		test $$status -eq 0 || exit $$status; \
		test -z "$$unformatted" || { printf 'gofumpt changes are required:\n%s\nRun: make prepare\n' "$$unformatted" >&2; exit 1; }
	@import_diff="$$(gci diff -s standard -s default -s "prefix(github.com/assurrussa/outbox)" $(CORE_GO_FILES) < /dev/null)"; \
		status=$$?; \
		test $$status -eq 0 || exit $$status; \
		test -z "$$import_diff" || { printf 'gci changes are required:\n%s\nRun: make prepare\n' "$$import_diff" >&2; exit 1; }

fmt-backends:
	gofumpt -l -w $(BACKEND_GO_FILES)
	@for d in $(BACKEND_DIRS); do \
		gci write -s standard -s default -s "prefix(github.com/assurrussa/outbox/$$d)" $$d || exit 1; \
	done

fmt-check-backends:
	@unformatted="$$(gofumpt -l $(BACKEND_GO_FILES))"; \
		status=$$?; \
		test $$status -eq 0 || exit $$status; \
		test -z "$$unformatted" || { printf 'gofumpt changes are required:\n%s\nRun: make prepare\n' "$$unformatted" >&2; exit 1; }
	@for d in $(BACKEND_DIRS); do \
		files="$$(find $$d -type f -name '*.go')"; \
		status=$$?; \
		test $$status -eq 0 || exit $$status; \
		import_diff="$$(gci diff -s standard -s default -s "prefix(github.com/assurrussa/outbox/$$d)" $$files < /dev/null)"; \
		status=$$?; \
		test $$status -eq 0 || exit $$status; \
		test -z "$$import_diff" || { printf 'gci changes are required in %s:\n%s\nRun: make prepare\n' "$$d" "$$import_diff" >&2; exit 1; }; \
	done

lint: lint-core lint-backends

lint-fix: lint-fix-core lint-fix-backends

lint-core:
	golangci-lint run -v --timeout=5m $(CORE_PKGS)

lint-fix-core:
	golangci-lint run -v --fix --timeout=5m $(CORE_PKGS)

lint-backends:
	@for d in $(BACKEND_DIRS); do \
		echo "==> lint $$d"; \
		(cd $$d && golangci-lint run -v --config ../../.golangci.yml --timeout=5m ./...) || exit 1; \
	done

lint-fix-backends:
	@for d in $(BACKEND_DIRS); do \
		echo "==> lint-fix $$d"; \
		(cd $$d && golangci-lint run -v --fix --config ../../.golangci.yml --timeout=5m ./...) || exit 1; \
	done

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

test-backends-standalone:
	@for d in $(BACKEND_DIRS); do \
		echo "==> test $$d standalone (GOWORK=off)"; \
		(cd $$d && GOWORK=off go test ./...) || exit 1; \
	done

test-examples:
	@for d in examples/*; do \
		if [ -d "$$d" ]; then \
			echo "==> build $$d (GOWORK=off)"; \
			(cd "$$d" && GOWORK=off go build ./...) || exit 1; \
		fi; \
	done

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
	@for d in $(BACKEND_DIRS) $(EXAMPLE_DIRS); do \
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
		GOWORK=off go mod tidy)
	@echo "==> refresh examples/base-app-pgsql"
	@(cd examples/base-app-pgsql && \
		GOWORK=off go mod tidy)
	@echo "==> refresh examples/base-app-mysql"
	@(cd examples/base-app-mysql && \
		GOWORK=off go mod tidy)
	@echo "==> refresh examples/base-app-sqlite"
	@(cd examples/base-app-sqlite && \
		GOWORK=off go mod tidy)
	@echo "==> refresh examples/base-app-picodata"
	@(cd examples/base-app-picodata && \
		GOWORK=off go mod tidy)

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
