#!/usr/bin/env bash
# Teardown for the ready-pods case. Idempotent and bounded; touches only itest-ch-rp* in the namespace.
set -uo pipefail

NS="${NAMESPACE:-default}"
rc=0

kubectl delete instances.core.openeverest.io itest-ch-rp -n "$NS" --ignore-not-found --wait=false || true
i=0
while [ "$i" -lt 60 ]; do
  kubectl get instances.core.openeverest.io itest-ch-rp -n "$NS" >/dev/null 2>&1 || break
  i=$((i + 1))
  sleep 5
done
if kubectl get instances.core.openeverest.io itest-ch-rp -n "$NS" >/dev/null 2>&1; then
  echo "timed out waiting for instance itest-ch-rp to be deleted" >&2
  rc=1
fi

for sel in clickhouse.altinity.com/chi=itest-ch-rp clickhouse-keeper.altinity.com/chk=itest-ch-rp-keeper; do
  kubectl delete pvc -n "$NS" -l "$sel" --ignore-not-found --wait=false || true
done
i=0
while [ "$i" -lt 24 ]; do
  left=$(kubectl get pvc -n "$NS" -l clickhouse.altinity.com/chi=itest-ch-rp -o name 2>/dev/null; \
         kubectl get pvc -n "$NS" -l clickhouse-keeper.altinity.com/chk=itest-ch-rp-keeper -o name 2>/dev/null)
  [ -z "$left" ] && break
  i=$((i + 1))
  sleep 5
done
if [ -n "${left:-}" ]; then
  echo "timed out waiting for PVCs to be deleted: $left" >&2
  rc=1
fi
exit "$rc"
