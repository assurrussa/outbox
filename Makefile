.DEFAULT_GOAL := check
GO_MODULE := $(shell go list -m)
GO_FILES := $(shell find . -type f -name '*.go')

check: picodata-render-all generate fmt vet lint test test-race cover-html
check-all: devup check test-integration devdown

generate:
	go generate ./...

fmt:
	go fmt ./...
	gofumpt -l -w $(GO_FILES)
	gci write -s standard -s default -s "prefix($(GO_MODULE))" .

lint:
	golangci-lint run -v --fix --timeout=5m ./...

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race -count=5 ./...

test-integration:
	go test -tags integration -race ./...

cover-html:
	@go test -coverprofile=./coverage.text -covermode=atomic $(shell go list ./...)
	@go tool cover -html=./coverage.text -o ./cover.html && rm ./coverage.text

bench-all:
	go test -bench=. -benchmem ./...

devup:
	docker compose up -d
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