/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package observability owns the operator's Prometheus metrics, OTel
// tracing setup, log/trace bridge, and pprof endpoint. Reconcilers
// import this package; cmd/main.go calls Init at startup.
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Label names locked in DESIGN-0001 § Metrics and confirmed in IMPL-0001
// Q3: no scan_namespace label (shadows scan when teams share names; or all
// scans live in the same namespace anyway).
const (
	LabelScan     = "scan"
	LabelPlatform = "platform"
	LabelResult   = "result"
)

// Result values for the result label on counters.
const (
	ResultSucceeded = "succeeded"
	ResultFailed    = "failed"
)

var (
	// RunsTotal counts terminal Runs by outcome.
	RunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "renovate_operator_runs_total",
			Help: "Total RenovateRun terminal transitions, by scan, platform, and result.",
		},
		[]string{LabelScan, LabelPlatform, LabelResult},
	)

	// DiscoveryErrorsTotal counts discovery failures (excluding rate-limit retries).
	DiscoveryErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "renovate_operator_discovery_errors_total",
			Help: "Total discovery failures, by scan and platform.",
		},
		[]string{LabelScan, LabelPlatform},
	)

	// ShardsFailedTotal counts shards that exhausted their backoffLimitPerIndex.
	ShardsFailedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "renovate_operator_shards_failed_total",
			Help: "Total worker shards that exhausted their backoff budget, by scan and platform.",
		},
		[]string{LabelScan, LabelPlatform},
	)

	// RunDurationSeconds is the wall-clock time from Run.Status.StartTime to CompletionTime.
	RunDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "renovate_operator_run_duration_seconds",
			Help:    "RenovateRun end-to-end duration, by scan and platform.",
			Buckets: prometheus.ExponentialBuckets(30, 2, 10), // 30s … ~4h
		},
		[]string{LabelScan, LabelPlatform},
	)

	// DiscoveryDurationSeconds is the wall-clock time of the Discovering phase.
	DiscoveryDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "renovate_operator_discovery_duration_seconds",
			Help:    "RenovateRun discovery-phase duration, by scan and platform.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1s … ~17m
		},
		[]string{LabelScan, LabelPlatform},
	)

	// ActiveRuns is a gauge of non-terminal Runs.
	ActiveRuns = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "renovate_operator_active_runs",
			Help: "Currently non-terminal RenovateRuns, by scan and platform.",
		},
		[]string{LabelScan, LabelPlatform},
	)

	// ShardCount is a gauge of the most recent Run's actualWorkers.
	ShardCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "renovate_run_shard_count",
			Help: "Worker shard count of the most recent Run, by scan and platform.",
		},
		[]string{LabelScan, LabelPlatform},
	)
)

// Register adds the operator's collectors to the controller-runtime metrics
// registry. Call once at startup; re-registering the same collectors panics.
func Register() {
	metrics.Registry.MustRegister(collectors()...)
}

// collectors returns the slice of every metric this package owns. Tests use
// this to construct a fresh registry for assertions.
func collectors() []prometheus.Collector {
	return []prometheus.Collector{
		RunsTotal,
		DiscoveryErrorsTotal,
		ShardsFailedTotal,
		RunDurationSeconds,
		DiscoveryDurationSeconds,
		ActiveRuns,
		ShardCount,
	}
}
