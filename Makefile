.DEFAULT_GOAL := check
BACKEND_DIRS := backends/mysql backends/sqlite backends/pgsql backends/picodata
CORE_PKGS := ./outbox/... ./shared/... ./tools/...
CORE_GO_FILES := $(shell find outbox shared tools -type f -name '*.go')
BACKEND_GO_FILES := $(shell find backends -type f -name '*.go')

check: picodata-render-all generate fmt vet lint test-core test-backends test-race-core cover-html
check-all: devup check test-integration-all devdown

generate:
	go generate ./...
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

cover-html:
	@go test -coverprofile=./coverage.text -covermode=atomic $(shell go list ./...)
	@go tool cover -html=./coverage.text -o ./cover.html && rm ./coverage.text

bench-all:
	go test -bench=. -benchmem ./...
	@for d in $(BACKEND_DIRS); do (cd $$d && go test -bench=. -benchmem ./...); done

devup:
	docker compose --profile mysql --profile pgsql --profile picodata up -d

devdown:
	docker compose down --remove-orphans

picodata-render-app:
	@PICO_CLUSTER_NAME=app_outbox \
	PICO_REPLICATION_FACTOR=1 \
	PICO_BUCKET_COUNT=1500 \
	PICO_INSTANCE_NAME=picodata-storage-1 \
	PICO_INSTANCE_DIR=/pico/data/picodata-storage-1 \
	PICO_PEER=picodata-storage-1:3301 \
	PICO_IPROTO_LISTEN=0.0.0.0:3301 \
	PICO_IPROTO_ADVERTISE=0.0.0.0:3301 \
	PICO_HTTP_LISTEN=0.0.0.0:8001 \
	PICO_PG_LISTEN=0.0.0.0:5001 \
	./docker/picodata/scripts/render_picodata_config.sh docker/picodata/cluster-storage.tmpl.yml docker/picodata/cluster-storage.yml

picodata-render-local:
	@PICO_CLUSTER_NAME=app_outbox_local \
	PICO_REPLICATION_FACTOR=1 \
	PICO_BUCKET_COUNT=1500 \
	PICO_INSTANCE_NAME=picodata-storage-local \
	PICO_INSTANCE_DIR=/pico/data/picodata-storage-local \
	PICO_PEER=0.0.0.0:3342 \
	PICO_IPROTO_LISTEN=0.0.0.0:3342 \
	PICO_IPROTO_ADVERTISE=0.0.0.0:3342 \
	PICO_HTTP_LISTEN=0.0.0.0:8042 \
	PICO_PG_LISTEN=0.0.0.0:5042 \
	./docker/picodata/scripts/render_picodata_config.sh docker/picodata/cluster-storage.tmpl.yml docker/picodata/cluster-storage-local.yml

picodata-render-tests:
	@PICO_CLUSTER_NAME=app_outbox_tests \
	PICO_REPLICATION_FACTOR=1 \
	PICO_BUCKET_COUNT=10 \
	PICO_INSTANCE_NAME=integration-picodata-tests \
	PICO_INSTANCE_DIR=/pico/data/integration-picodata-tests \
	PICO_PEER=0.0.0.0:3349 \
	PICO_IPROTO_LISTEN=0.0.0.0:3349 \
	PICO_IPROTO_ADVERTISE=0.0.0.0:3349 \
	PICO_HTTP_LISTEN=0.0.0.0:8049 \
	PICO_PG_LISTEN=0.0.0.0:5049 \
	./docker/picodata/scripts/render_picodata_config.sh docker/picodata/cluster-storage.tmpl.yml docker/picodata/cluster-storage-tests.yml

picodata-render-all: picodata-render-app picodata-render-local picodata-render-tests
