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

  # wait until the cluster reports ok, every replica has finished its initial sync and all nodes agree
  # on the topology; tests that fail over to a replica need it known and in sync (CLUSTER SLOTS omits
  # replicas whose link is down or that the queried node has not heard about yet)
  for _ in $(seq 1 150); do
    if "$REDIS_CLI" -p "$BASE_PORT" cluster info | grep -q 'cluster_state:ok' && replicas_synced && views_agree; then
      echo "cluster up: $(IFS=,; echo "${addrs[*]}")"
      return 0
    fi
    sleep 0.2
  done
  echo "cluster did not become ready (state ok and all replicas synced)" >&2
  return 1
}

# views_agree succeeds when every node reports the same topology (ignoring the per-observer "myself"
# marker) and every master has its replicas attached, i.e. gossip has converged.
views_agree() {
  local p want="" got masters replicas
  for p in $(ports); do
    got=$("$REDIS_CLI" -p "$p" cluster nodes | sed 's/myself,//' | awk '{print $1, $3, $4, $8, $9}' | sort) || return 1
    if [ -z "$want" ]; then want="$got"; elif [ "$got" != "$want" ]; then return 1; fi
  done
  masters=$(grep -c ' master ' <<<"$want" || true)
  replicas=$(grep -c ' slave ' <<<"$want" || true)
  [ "$masters" -gt 0 ] && [ "$replicas" -eq $((masters * REPLICAS)) ]
}

# replicas_synced succeeds when no node is a replica with a down master link.
replicas_synced() {
  local p info
  for p in $(ports); do
    info=$("$REDIS_CLI" -p "$p" info replication) || return 1
    if grep -q 'role:slave' <<<"$info" && ! grep -q 'master_link_status:up' <<<"$info"; then
      return 1
    fi
  done
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
