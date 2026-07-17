#!/usr/bin/env bash
# Model-check all TLA+ instances of the SETTLE search (FORMAL_SPEC 6.1, 7.1, 7.2,
# plus the competing-derivation min-selection stressor).
# Exits 0 iff ALL configs report "No error has been found."
set -euo pipefail
cd "$(dirname "$0")"

JAR="${TLA_TOOLS_JAR:-$HOME/.local/lib/tla2tools.jar}"
[ -f "$JAR" ] || { echo "tla2tools.jar not found at $JAR; see README.md"; exit 1; }

run_cfg() {
  local cfg="$1"
  echo "=== TLC: PlannerSearch.tla -config ${cfg} ==="
  java -XX:+UseParallelGC -cp "$JAR" tlc2.TLC -deadlock PlannerSearch.tla -config "${cfg}"
  echo
}

run_cfg PlannerSearch.cfg
run_cfg PlannerSearchEntityJump.cfg
run_cfg PlannerSearchCompeting.cfg

echo "All configs model-checked clean."
