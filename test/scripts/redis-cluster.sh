#!/usr/bin/env bash
# Local OSS Redis Cluster for running the integration suite without Docker.
#
#   test/scripts/redis-cluster.sh start    # 3 masters + 3 replicas on 127.0.0.1:7000-7005
#   test/scripts/redis-cluster.sh stop
#   test/scripts/redis-cluster.sh status
#
# Then run the suite against it:
#   export REDIS_CLUSTER_ADDRS=127.0.0.1:7000,127.0.0.1:7001,127.0.0.1:7002
#   go test ./test/...
#
# Environment overrides: CLUSTER_BASE_PORT (7000), CLUSTER_NODES (6), CLUSTER_REPLICAS (1),
# CLUSTER_DIR (a temp dir), REDIS_SERVER / REDIS_CLI (binaries on PATH).
set -euo pipefail

BASE_PORT="${CLUSTER_BASE_PORT:-7000}"
NODES="${CLUSTER_NODES:-6}"
REPLICAS="${CLUSTER_REPLICAS:-1}"
DIR="${CLUSTER_DIR:-${TMPDIR:-/tmp}/redis-stream-client-cluster}"
REDIS_SERVER="${REDIS_SERVER:-redis-server}"
REDIS_CLI="${REDIS_CLI:-redis-cli}"

ports() { seq "$BASE_PORT" $((BASE_PORT + NODES - 1)); }

start() {
  mkdir -p "$DIR"
  for p in $(ports); do
    mkdir -p "$DIR/$p"
    "$REDIS_SERVER" --port "$p" --bind 127.0.0.1 --daemonize yes \
      --dir "$DIR/$p" --logfile "$DIR/$p/redis.log" --pidfile "$DIR/$p/redis.pid" \
      --cluster-enabled yes --cluster-config-file "$DIR/$p/nodes.conf" \
      --cluster-node-timeout 3000 --appendonly no --save "" \
      --protected-mode no >/dev/null
  done

  for p in $(ports); do
    until "$REDIS_CLI" -p "$p" ping >/dev/null 2>&1; do sleep 0.1; done
  done

  local addrs=()
  for p in $(ports); do addrs+=("127.0.0.1:$p"); done
  "$REDIS_CLI" --cluster create "${addrs[@]}" --cluster-replicas "$REPLICAS" --cluster-yes >/dev/null

  # wait until the cluster reports ok
  for _ in $(seq 1 100); do
    if "$REDIS_CLI" -p "$BASE_PORT" cluster info | grep -q 'cluster_state:ok'; then
      echo "cluster up: $(IFS=,; echo "${addrs[*]}")"
      return 0
    fi
    sleep 0.2
  done
  echo "cluster did not reach state ok" >&2
  return 1
}

stop() {
  for p in $(ports); do
    "$REDIS_CLI" -p "$p" shutdown nosave >/dev/null 2>&1 || true
  done
  rm -rf "$DIR"
}

status() {
  "$REDIS_CLI" -p "$BASE_PORT" cluster nodes
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  status) status ;;
  *) echo "usage: $0 {start|stop|status}" >&2; exit 2 ;;
esac
