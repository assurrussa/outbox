#!/usr/bin/env sh
set -eu

template=${1:-}
output=${2:-}

if [ -z "$template" ] || [ -z "$output" ]; then
  echo "usage: $0 <template> <output>" >&2
  exit 1
fi

: "${PICO_CLUSTER_NAME:=app_outbox}"
: "${PICO_REPLICATION_FACTOR:=1}"
: "${PICO_BUCKET_COUNT:=1500}"
: "${PICO_INSTANCE_NAME:=picodata-storage-1}"
: "${PICO_INSTANCE_DIR:=/pico/data/picodata-storage-1}"
: "${PICO_PEER:=picodata-storage-1:3301}"
: "${PICO_IPROTO_LISTEN:=0.0.0.0:3301}"
: "${PICO_IPROTO_ADVERTISE:=picodata-storage-1:3301}"
: "${PICO_HTTP_LISTEN:=0.0.0.0:8001}"
: "${PICO_PG_LISTEN:=0.0.0.0:5001}"

mkdir -p "$(dirname "$output")"

sed -e "s|__PICO_CLUSTER_NAME__|${PICO_CLUSTER_NAME}|g" \
  -e "s|__PICO_REPLICATION_FACTOR__|${PICO_REPLICATION_FACTOR}|g" \
  -e "s|__PICO_BUCKET_COUNT__|${PICO_BUCKET_COUNT}|g" \
  -e "s|__PICO_INSTANCE_NAME__|${PICO_INSTANCE_NAME}|g" \
  -e "s|__PICO_INSTANCE_DIR__|${PICO_INSTANCE_DIR}|g" \
  -e "s|__PICO_PEER__|${PICO_PEER}|g" \
  -e "s|__PICO_IPROTO_LISTEN__|${PICO_IPROTO_LISTEN}|g" \
  -e "s|__PICO_IPROTO_ADVERTISE__|${PICO_IPROTO_ADVERTISE}|g" \
  -e "s|__PICO_HTTP_LISTEN__|${PICO_HTTP_LISTEN}|g" \
  -e "s|__PICO_PG_LISTEN__|${PICO_PG_LISTEN}|g" \
  "$template" > "$output"
