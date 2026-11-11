# Helpers sourced by the query-limits steps. Talks only to the itest-ch-ql Instance.
NS="${NAMESPACE:-default}"
NAME="itest-ch-ql"
PW=$(kubectl get secret "$NAME-credentials" -n "$NS" -o jsonpath='{.data.password}' | base64 -d)
POD=$(kubectl get pods -n "$NS" -l "clickhouse.altinity.com/chi=$NAME" -o jsonpath='{.items[0].metadata.name}')
[ -n "$POD" ] || { echo "ClickHouse pod not found for $NAME" >&2; exit 1; }

# ch USER PASSWORD QUERY: run a query, print stdout, keep stderr in $ERR.
ERR=$(mktemp)
ch() { kubectl exec -n "$NS" "$POD" -c clickhouse -- clickhouse-client --user "$1" --password "$2" -q "$3" 2>"$ERR"; }
admin() { ch admin "$PW" "$1"; }

# expect_code CODE USER PASSWORD QUERY: the query must fail with ClickHouse error CODE.
expect_code() {
  local code="$1"; shift
  if ch "$@" >/dev/null; then
    echo "expected error code $code but the query succeeded: $3" >&2
    return 1
  fi
  if ! grep -q "Code: $code\." "$ERR"; then
    echo "expected error code $code for: $3; got:" >&2
    cat "$ERR" >&2
    return 1
  fi
  echo "got expected Code: $code for: $3"
}

restarts() {
  kubectl get pod "$POD" -n "$NS" -o jsonpath='{.status.containerStatuses[?(@.name=="clickhouse")].restartCount}'
}
