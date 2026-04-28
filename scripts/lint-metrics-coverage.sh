#!/usr/bin/env bash
#
# lint-metrics-coverage.sh — fail when a metric defined in
# internal/observability/metrics.go is not referenced anywhere under
# contrib/{grafana,prometheus} or dist/chart/templates/extra/prometheusrule.yaml.
#
# A metric can be exempted by adding "// metric:internal" on the same line
# as its `Name:` declaration.
#
# Usage:  ./scripts/lint-metrics-coverage.sh
#
# Per IMPL-0001 Phase 6.6 / DESIGN-0001 § Metrics.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
METRICS_FILE="${REPO_ROOT}/internal/observability/metrics.go"
SEARCH_PATHS=(
	"${REPO_ROOT}/contrib"
	"${REPO_ROOT}/dist/chart/templates/extra/prometheusrule.yaml"
)

if [[ ! -f "${METRICS_FILE}" ]]; then
	echo "lint-metrics-coverage: ${METRICS_FILE} not found" >&2
	exit 1
fi

# Extract metric names from `Name: "renovate_..."` lines, dropping any line
# that ends with `// metric:internal`.
mapfile -t METRICS < <(
	grep -E '^[[:space:]]*Name:[[:space:]]*"' "${METRICS_FILE}" |
		grep -v '// metric:internal' |
		sed -E 's/.*"([^"]+)".*/\1/'
)

if [[ ${#METRICS[@]} -eq 0 ]]; then
	echo "lint-metrics-coverage: no metrics found in ${METRICS_FILE}" >&2
	exit 1
fi

missing=0
for metric in "${METRICS[@]}"; do
	if ! grep -rqE "${metric}(_bucket|_count|_sum)?" "${SEARCH_PATHS[@]}"; then
		printf "MISSING  %s\n" "${metric}" >&2
		missing=$((missing + 1))
	fi
done

if [[ ${missing} -gt 0 ]]; then
	cat <<-EOF >&2

	${missing} metric(s) above are defined in internal/observability/metrics.go
	but never referenced under contrib/ or the chart's PrometheusRule.

	Either:
	  - add a panel/alert/recording-rule that uses the metric, or
	  - mark it internal-only by appending "// metric:internal" to the Name: line.
	EOF
	exit 1
fi

echo "lint-metrics-coverage: all ${#METRICS[@]} metrics referenced."
