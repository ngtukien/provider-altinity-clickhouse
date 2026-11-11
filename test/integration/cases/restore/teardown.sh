#!/usr/bin/env bash
# Full ordered teardown for the restore integration case.
# Idempotent and bounded: safe to run multiple times, on failure or success.
# Touches only itest-ch-restore* resources in the default namespace.
set -euo pipefail

NS="${NAMESPACE:-default}"
rc=0

# 1. Stop new backups: remove schedule from Instance spec and delete ClickHouseBackupSchedules
for inst in itest-ch-restore itest-ch-restore-target itest-ch-restore-sharded; do
  kubectl patch instance.core.openeverest.io "$inst" -n "$NS" --type=merge -p '{"spec":{"backup":{"storages":[{"storageRef":{"name":"itest-ch-restore-backup-storage"},"schedules":[]}]}}}' 2>/dev/null || true
done
kubectl delete clickhousebackupschedules.clickhouse.altinity.com itest-ch-restore-hourly -n "$NS" --ignore-not-found --timeout=60s || true
for s in $(kubectl get clickhousebackupschedules.clickhouse.altinity.com -n "$NS" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true); do
  case "$s" in
    itest-ch-restore*)
      kubectl delete clickhousebackupschedules.clickhouse.altinity.com "$s" -n "$NS" --ignore-not-found --timeout=60s || true
      ;;
  esac
done

# 2. Delete the case's Restore CRs and wait for them to be gone (timeout 120s)
kubectl delete restores.backup.openeverest.io \
  itest-ch-restore-a \
  itest-ch-restore-b-refused \
  itest-ch-restore-b-overwrite \
  itest-ch-restore-c-target \
  itest-ch-restore-d-sched \
  itest-ch-restore-sharded \
  -n "$NS" --ignore-not-found --wait=false || true
for r in $(kubectl get restores.backup.openeverest.io -n "$NS" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.instanceRef.name}{"\n"}{end}' 2>/dev/null | awk '$1 ~ /^itest-ch-restore/ || $2 ~ /^itest-ch-restore/ {print $1}' || true); do
  kubectl delete restores.backup.openeverest.io "$r" -n "$NS" --ignore-not-found --wait=false || true
done
i=0
remaining=""
while [ "$i" -lt 24 ]; do
  remaining=$(kubectl get restores.backup.openeverest.io -n "$NS" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.instanceRef.name}{"\n"}{end}' 2>/dev/null | awk '$1 ~ /^itest-ch-restore/ || $2 ~ /^itest-ch-restore/ {print $1}' || true)
  if [ -z "$remaining" ]; then
    break
  fi
  i=$((i + 1))
  sleep 5
done
if [ -n "$remaining" ]; then
  echo "timed out waiting for restores to be deleted: $remaining" >&2
  rc=1
fi

# 3. Delete every Backup CR of the case's Instances and wait for them to be gone (timeout 300s)
kubectl delete backups.backup.openeverest.io \
  itest-ch-restore-backup-a \
  itest-ch-restore-backup-sharded \
  -n "$NS" --ignore-not-found --wait=false || true
for b in $(kubectl get backups.backup.openeverest.io -n "$NS" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.origin.instanceRef.name}{"\n"}{end}' 2>/dev/null | awk '$1 ~ /^itest-ch-restore/ || $2 ~ /^itest-ch-restore/ {print $1}' || true); do
  kubectl delete backups.backup.openeverest.io "$b" -n "$NS" --ignore-not-found --wait=false || true
done
i=0
remaining=""
while [ "$i" -lt 60 ]; do
  remaining=$(kubectl get backups.backup.openeverest.io -n "$NS" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.origin.instanceRef.name}{"\n"}{end}' 2>/dev/null | awk '$1 ~ /^itest-ch-restore/ || $2 ~ /^itest-ch-restore/ {print $1}' || true)
  if [ -z "$remaining" ]; then
    break
  fi
  i=$((i + 1))
  sleep 5
done
if [ -n "$remaining" ]; then
  echo "timed out waiting for backups to be deleted: $remaining" >&2
  rc=1
fi

# 4. Delete the Instances and wait for them to be gone (timeout 300s)
kubectl delete instances.core.openeverest.io \
  itest-ch-restore \
  itest-ch-restore-target \
  itest-ch-restore-sharded \
  -n "$NS" --ignore-not-found --wait=false || true
i=0
remaining=""
while [ "$i" -lt 60 ]; do
  remaining=$(kubectl get instances.core.openeverest.io -n "$NS" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null | grep -E '^itest-ch-restore' || true)
  if [ -z "$remaining" ]; then
    break
  fi
  i=$((i + 1))
  sleep 5
done
if [ -n "$remaining" ]; then
  echo "timed out waiting for instances to be deleted: $remaining" >&2
  rc=1
fi

# 5. Delete PVCs by the existing label selectors
for sel in \
  "clickhouse.altinity.com/chi=itest-ch-restore" \
  "clickhouse.altinity.com/chi=itest-ch-restore-target" \
  "clickhouse.altinity.com/chi=itest-ch-restore-sharded" \
  "clickhouse-keeper.altinity.com/chk=itest-ch-restore-sharded-keeper"; do
  kubectl delete pvc -n "$NS" -l "$sel" --ignore-not-found --wait=false || true
done
i=0
remaining=""
while [ "$i" -lt 12 ]; do
  remaining=""
  for sel in \
    "clickhouse.altinity.com/chi=itest-ch-restore" \
    "clickhouse.altinity.com/chi=itest-ch-restore-target" \
    "clickhouse.altinity.com/chi=itest-ch-restore-sharded" \
    "clickhouse-keeper.altinity.com/chk=itest-ch-restore-sharded-keeper"; do
    names=$(kubectl get pvc -n "$NS" -l "$sel" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
    if [ -n "$names" ]; then
      remaining="${remaining:+$remaining }$names"
    fi
  done
  if [ -z "$remaining" ]; then
    break
  fi
  i=$((i + 1))
  sleep 5
done
if [ -n "$remaining" ]; then
  echo "timed out waiting for PVCs to be deleted: $remaining" >&2
  rc=1
fi

# 6. Delete BackupStorage
kubectl delete backupstorages.backup.openeverest.io itest-ch-restore-backup-storage -n "$NS" --ignore-not-found --timeout=60s || true

# 7. Delete S3 store (Deployment, Service, Job, Secret) last
kubectl delete deployment,service,job,secret -l app=itest-ch-restore-minio -n "$NS" --ignore-not-found --timeout=60s || true

exit "$rc"
