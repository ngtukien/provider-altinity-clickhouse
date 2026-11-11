#!/usr/bin/env bash
# Teardown for the query-limits case: idempotent, bounded, touches only itest-ch-ql in default.
set -uo pipefail
NS="${NAMESPACE:-default}"
rc=0

kubectl delete instances.core.openeverest.io itest-ch-ql -n "$NS" --ignore-not-found --wait=false || true
i=0
while [ "$i" -lt 60 ] && kubectl get instances.core.openeverest.io itest-ch-ql -n "$NS" >/dev/null 2>&1; do
  i=$((i + 1)); sleep 5
done
if kubectl get instances.core.openeverest.io itest-ch-ql -n "$NS" >/dev/null 2>&1; then
  echo "timed out waiting for Instance itest-ch-ql to be deleted" >&2
  rc=1
fi

kubectl delete pvc -n "$NS" -l clickhouse.altinity.com/chi=itest-ch-ql --ignore-not-found --wait=false || true
i=0
while [ "$i" -lt 12 ] && [ -n "$(kubectl get pvc -n "$NS" -l clickhouse.altinity.com/chi=itest-ch-ql -o name 2>/dev/null)" ]; do
  i=$((i + 1)); sleep 5
done
if [ -n "$(kubectl get pvc -n "$NS" -l clickhouse.altinity.com/chi=itest-ch-ql -o name 2>/dev/null)" ]; then
  echo "timed out waiting for itest-ch-ql PVCs to be deleted" >&2
  rc=1
fi
exit "$rc"
