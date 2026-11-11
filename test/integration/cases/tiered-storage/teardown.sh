#!/usr/bin/env bash
# Ordered teardown for the tiered-storage case: Instance, PVCs, BackupStorage, S3 store.
# Idempotent and bounded; touches only itest-ch-ts* resources in the default namespace.
set -uo pipefail

NS="${NAMESPACE:-default}"
rc=0

kubectl delete instances.core.openeverest.io itest-ch-ts -n "$NS" --ignore-not-found --wait=false || true
i=0
while [ "$i" -lt 60 ] && kubectl get instances.core.openeverest.io itest-ch-ts -n "$NS" >/dev/null 2>&1; do
  i=$((i + 1))
  sleep 5
done
if kubectl get instances.core.openeverest.io itest-ch-ts -n "$NS" >/dev/null 2>&1; then
  echo "timed out waiting for Instance itest-ch-ts to be deleted" >&2
  rc=1
fi

kubectl delete pvc -n "$NS" -l clickhouse.altinity.com/chi=itest-ch-ts --ignore-not-found --timeout=120s || rc=1
kubectl delete backupstorages.backup.openeverest.io itest-ch-ts-storage -n "$NS" --ignore-not-found --timeout=60s || rc=1
kubectl delete deployment,service,job,secret -l app=itest-ch-ts-s3 -n "$NS" --ignore-not-found --timeout=60s || rc=1

exit "$rc"
