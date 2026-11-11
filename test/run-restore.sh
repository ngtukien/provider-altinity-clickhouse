#!/usr/bin/env bash
set -uo pipefail

# Determine repository root from script directory
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
cd -- "$REPO_ROOT"

# Ensure output directory is provided
if [ -z "${MINION_TEST_OUT:-}" ]; then
  echo "ERROR: MINION_TEST_OUT environment variable is required" >&2
  exit 2
fi

mkdir -p "$MINION_TEST_OUT"

# Resolve LOCALBIN: caller environment > $REPO_ROOT/bin > main git checkout bin
if [ -z "${LOCALBIN:-}" ]; then
  if [ -d "$REPO_ROOT/bin" ]; then
    LOCALBIN="$REPO_ROOT/bin"
  elif git rev-parse --git-common-dir >/dev/null 2>&1; then
    common_dir="$(git rev-parse --git-common-dir)"
    main_root="$(cd "$common_dir/.." && pwd)"
    if [ -d "$main_root/bin" ]; then
      LOCALBIN="$main_root/bin"
    else
      LOCALBIN="$REPO_ROOT/bin"
    fi
  else
    LOCALBIN="$REPO_ROOT/bin"
  fi
fi

SUMMARY_TSV="$MINION_TEST_OUT/summary.tsv"
printf 'step\tresult\texit_code\tlog\n' > "$SUMMARY_TSV"

ANY_FAILED=0

record_step() {
  local step="$1"
  local result="$2"
  local code="$3"
  local log_file="$4"

  printf '%s\t%s\t%s\t%s\n' "$step" "$result" "$code" "$log_file" >> "$SUMMARY_TSV"
  if [ "$result" = "fail" ]; then
    ANY_FAILED=1
  fi
}

# 1. build — go build ./...
step="build"
log="$MINION_TEST_OUT/build.log"
echo "==> Step: $step"
if go build ./... > "$log" 2>&1; then
  record_step "$step" "pass" "0" "$log"
else
  code=$?
  record_step "$step" "fail" "$code" "$log"
fi

# 2. unit — go test ./...
step="unit"
log="$MINION_TEST_OUT/unit.log"
echo "==> Step: $step"
if go test ./... > "$log" 2>&1; then
  record_step "$step" "pass" "0" "$log"
else
  code=$?
  record_step "$step" "fail" "$code" "$log"
fi

# 3. lint — make lint LOCALBIN=$LOCALBIN
step="lint"
log="$MINION_TEST_OUT/lint.log"
echo "==> Step: $step"
if make lint LOCALBIN="$LOCALBIN" > "$log" 2>&1; then
  record_step "$step" "pass" "0" "$log"
else
  code=$?
  record_step "$step" "fail" "$code" "$log"
fi

# 4. codegen — git worktree under $MINION_TEST_OUT, make generate LOCALBIN=$LOCALBIN, git diff --exit-code
step="codegen"
log="$MINION_TEST_OUT/codegen.log"
echo "==> Step: $step"
codegen_wt="$MINION_TEST_OUT/codegen-wt"
git worktree remove --force "$codegen_wt" >/dev/null 2>&1 || true
rm -rf "$codegen_wt"

if git worktree add --detach "$codegen_wt" HEAD > "$log" 2>&1; then
  codegen_code=0
  if make -C "$codegen_wt" generate LOCALBIN="$LOCALBIN" >> "$log" 2>&1 && \
     git -C "$codegen_wt" diff --exit-code -- config/ charts/provider-altinity-clickhouse/generated/ >> "$log" 2>&1; then
    codegen_code=0
  else
    codegen_code=$?
  fi
  git worktree remove --force "$codegen_wt" >> "$log" 2>&1 || rm -rf "$codegen_wt"
  if [ "$codegen_code" -eq 0 ]; then
    record_step "$step" "pass" "0" "$log"
  else
    record_step "$step" "fail" "$codegen_code" "$log"
  fi
else
  code=$?
  record_step "$step" "fail" "$code" "$log"
fi

# 5. chart — if $OPERATOR_CHART_TGZ points to existing file, template chart and check rules & CRD
step="chart"
log="$MINION_TEST_OUT/chart.log"
echo "==> Step: $step"
if [ -z "${OPERATOR_CHART_TGZ:-}" ]; then
  echo "OPERATOR_CHART_TGZ not set" > "$log"
  record_step "$step" "skip" "0" "$log"
elif [ ! -f "$OPERATOR_CHART_TGZ" ]; then
  echo "OPERATOR_CHART_TGZ file not found: $OPERATOR_CHART_TGZ" > "$log"
  record_step "$step" "skip" "0" "$log"
else
  chart_tmp="$MINION_TEST_OUT/chart-temp"
  rm -rf "$chart_tmp"
  mkdir -p "$chart_tmp"
  cp -r "$REPO_ROOT/charts/provider-altinity-clickhouse" "$chart_tmp/"
  mkdir -p "$chart_tmp/provider-altinity-clickhouse/charts"
  cp "$OPERATOR_CHART_TGZ" "$chart_tmp/provider-altinity-clickhouse/charts/"

  rendered_file="$chart_tmp/rendered.yaml"
  rendered_rbac="$chart_tmp/rendered-rbac.yaml"
  helm_stderr="$chart_tmp/helm-stderr.log"
  helm_code=0
  if helm template test-release "$chart_tmp/provider-altinity-clickhouse" --include-crds > "$rendered_file" 2> "$helm_stderr" && \
     helm template test-release "$chart_tmp/provider-altinity-clickhouse" --show-only templates/clusterrole.yaml > "$rendered_rbac" 2>> "$helm_stderr"; then
    helm_code=0
  else
    helm_code=$?
  fi

  # Log helm stderr if any was produced, but do not write the rendered YAML to avoid leaking TLS private keys
  if [ -s "$helm_stderr" ]; then
    cat "$helm_stderr" > "$log"
  else
    : > "$log"
  fi

  if [ "$helm_code" -ne 0 ]; then
    echo "ERROR: helm template failed with exit code $helm_code" >> "$log"
    rm -rf "$chart_tmp"
    record_step "$step" "fail" "$helm_code" "$log"
  else
    # Parse rendered YAML and verify specific RBAC rule in provider ClusterRole and CRD in full render
    py_check_code=0
    python3 -c '
import sys, yaml

rbac_file = sys.argv[1]
crd_file = sys.argv[2]

rbac_found = False
with open(rbac_file, "r") as f:
    rbac_docs = list(yaml.safe_load_all(f))

for doc in rbac_docs:
    if not isinstance(doc, dict):
        continue
    kind = doc.get("kind", "")
    if kind in ("ClusterRole", "Role"):
        rules = doc.get("rules", []) or []
        for rule in rules:
            api_groups = rule.get("apiGroups", []) or []
            resources = rule.get("resources", []) or []
            verbs = set(rule.get("verbs", []) or [])
            has_api = ("clickhouse.altinity.com" in api_groups or "*" in api_groups)
            has_res = ("clickhouserestores" in resources or "*" in resources)
            has_verbs = ("*" in verbs or {"create", "get", "delete"}.issubset(verbs))
            if has_api and has_res and has_verbs:
                rbac_found = True

crd_found = False
with open(crd_file, "r") as f:
    crd_docs = list(yaml.safe_load_all(f))

for doc in crd_docs:
    if not isinstance(doc, dict):
        continue
    kind = doc.get("kind", "")
    metadata = doc.get("metadata", {})
    name = metadata.get("name", "")
    if kind == "CustomResourceDefinition" and name == "clickhouserestores.clickhouse.altinity.com":
        crd_found = True

errs = []
if not rbac_found:
    errs.append("missing provider ClusterRole/Role RBAC rule for clickhouserestores (apiGroup: clickhouse.altinity.com, verbs: create, get, delete) in templates/clusterrole.yaml")
else:
    print("Found provider ClusterRole RBAC rule for clickhouserestores (apiGroup: clickhouse.altinity.com, verbs: create, get, delete)")

if not crd_found:
    errs.append("missing CustomResourceDefinition clickhouserestores.clickhouse.altinity.com")
else:
    print("Found CustomResourceDefinition clickhouserestores.clickhouse.altinity.com")

if errs:
    for e in errs:
        sys.stderr.write("ERROR: " + e + "\n")
    sys.exit(1)
' "$rendered_rbac" "$rendered_file" >> "$log" 2>&1 || py_check_code=$?

    rm -rf "$chart_tmp"

    if [ "$py_check_code" -ne 0 ]; then
      record_step "$step" "fail" "$py_check_code" "$log"
    else
      record_step "$step" "pass" "0" "$log"
    fi
  fi
fi

# 6. chainsaw-lint — if chainsaw on PATH: lint every YAML of cases/restore
step="chainsaw-lint"
log="$MINION_TEST_OUT/chainsaw-lint.log"
echo "==> Step: $step"
if ! command -v chainsaw >/dev/null 2>&1; then
  echo "chainsaw not found on PATH" > "$log"
  record_step "$step" "skip" "0" "$log"
else
  lint_code=0
  for yf in "$REPO_ROOT/test/integration/cases/restore"/*.yaml "$REPO_ROOT/test/integration/cases/restore"/*.yml; do
    [ -e "$yf" ] || continue
    base=$(basename "$yf")
    if [ "$base" = "chainsaw-test.yaml" ]; then
      echo "--- Linting test: $yf ---" >> "$log"
      if ! chainsaw lint test -f "$yf" >> "$log" 2>&1; then
        lint_code=1
      fi
    else
      echo "--- Parsing YAML: $yf ---" >> "$log"
      if ! python3 -c 'import yaml, sys; list(yaml.safe_load_all(open(sys.argv[1])))' "$yf" >> "$log" 2>&1; then
        lint_code=1
      fi
    fi
  done
  if [ "$lint_code" -eq 0 ]; then
    record_step "$step" "pass" "0" "$log"
  else
    record_step "$step" "fail" "1" "$log"
  fi
fi

# 7. e2e-restore — only if chainsaw on PATH and kubectl cluster-info succeeds
step="e2e-restore"
log="$MINION_TEST_OUT/e2e-restore.log"
echo "==> Step: $step"
if ! command -v chainsaw >/dev/null 2>&1; then
  echo "chainsaw not found on PATH" > "$log"
  record_step "$step" "skip" "0" "$log"
elif ! kubectl cluster-info --request-timeout=5s >/dev/null 2>&1; then
  echo "Kubernetes cluster unreachable (kubectl cluster-info failed)" > "$log"
  record_step "$step" "skip" "0" "$log"
else
  if chainsaw test test/integration/cases/restore --config test/integration/chainsaw-config.yaml > "$log" 2>&1; then
    record_step "$step" "pass" "0" "$log"
  else
    code=$?
    record_step "$step" "fail" "$code" "$log"
  fi
fi

# 8. e2e-backup — only if chainsaw on PATH and kubectl cluster-info succeeds
step="e2e-backup"
log="$MINION_TEST_OUT/e2e-backup.log"
echo "==> Step: $step"
if ! command -v chainsaw >/dev/null 2>&1; then
  echo "chainsaw not found on PATH" > "$log"
  record_step "$step" "skip" "0" "$log"
elif ! kubectl cluster-info --request-timeout=5s >/dev/null 2>&1; then
  echo "Kubernetes cluster unreachable (kubectl cluster-info failed)" > "$log"
  record_step "$step" "skip" "0" "$log"
else
  if chainsaw test test/integration/cases/backup --config test/integration/chainsaw-config.yaml > "$log" 2>&1; then
    record_step "$step" "pass" "0" "$log"
  else
    code=$?
    record_step "$step" "fail" "$code" "$log"
  fi
fi

if [ "$ANY_FAILED" -ne 0 ]; then
  exit 1
fi

exit 0
