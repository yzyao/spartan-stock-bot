#!/usr/bin/env bash
set -euo pipefail
experiment_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
mode="${1:-both}"
case "$mode" in both|stockvps|vpsknow) ;; *) echo 'Usage: run.sh [both|stockvps|vpsknow] [--all]' >&2; exit 2 ;; esac
if (($#)); then shift; fi
for arg in "$@"; do
  [[ "$arg" == '--all' ]] || { echo "Unknown argument: $arg" >&2; exit 2; }
done
compose=(docker compose --env-file /dev/null --project-directory "$experiment_dir" -p "spartan-cf-probe-$$" -f "$experiment_dir/compose.yml")
cleanup() {
  probe_exit=$?
  trap - EXIT
  if ((probe_exit != 0)) && [[ "$mode" != vpsknow ]]; then
    "${compose[@]}" logs --no-color --tail=30 solver >&2 || true
  fi
  "${compose[@]}" down --remove-orphans >/dev/null 2>&1 || true
  exit "$probe_exit"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
"${compose[@]}" build probe
if [[ "$mode" != vpsknow ]]; then "${compose[@]}" up -d solver; fi
"${compose[@]}" run --rm --no-deps probe "$mode" "$@"
