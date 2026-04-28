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

// Package sharding builds the per-Run shard ConfigMap data: it computes the
// worker count, round-robin-assigns repos across workers, and gzips
// individual shard files when their size would push the ConfigMap past the
// etcd safety margin.
//
// Everything here is pure: no Kubernetes client calls, no I/O, no clock.
// The Run reconciler turns the Result into a real *corev1.ConfigMap.
package sharding

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// gzipThresholdBytes is the per-shard size above which the builder emits a
// gzip+base64 .json.gz key instead of plain .json. ConfigMap data values are
// limited to ~1 MiB by etcd; 900 KiB leaves headroom for keys, metadata,
// and a few percent of base64 expansion.
const gzipThresholdBytes = 900 * 1024

// ShardKeyJSON returns the ConfigMap key for an uncompressed shard at index i.
func ShardKeyJSON(index int) string {
	return fmt.Sprintf("shard-%04d.json", index)
}

// ShardKeyGzip returns the ConfigMap key for a gzip+base64 shard at index i.
func ShardKeyGzip(index int) string {
	return fmt.Sprintf("shard-%04d.json.gz", index)
}

// WorkerBounds is the minimum subset of the Scan's WorkersSpec the builder
// needs. Decoupling from the v1alpha1 type keeps this package importable
// from tests that don't want to construct a full RenovateScan.
type WorkerBounds struct {
	MinWorkers     int32
	MaxWorkers     int32
	ReposPerWorker int32
}

// ShardPayload is the JSON shape written into each shard-*.json key. The
// worker entrypoint reads .repos and exports it as RENOVATE_REPOSITORIES.
type ShardPayload struct {
	// Index is the 0-based shard index, equal to JOB_COMPLETION_INDEX.
	Index int `json:"index"`
	// Total is ActualWorkers, included so workers can sanity-check the plan.
	Total int `json:"total"`
	// Repos is the slice of repo slugs assigned to this shard.
	Repos []string `json:"repos"`
}

// Result is everything Build returns: a deterministic worker count, the
// ConfigMap data map (one key per shard, gzipped above the threshold), and
// counters for status surface.
type Result struct {
	// ActualWorkers is clamp(ceil(len(repos)/ReposPerWorker), Min, Max).
	ActualWorkers int32

	// Data is keyed by ShardKeyJSON(i) or ShardKeyGzip(i) and ready to drop
	// into a corev1.ConfigMap.Data map.
	Data map[string]string

	// Compressed reports per shard index whether its key is gzipped. Useful
	// for tests and for the worker entrypoint script's "look at .json then
	// .json.gz" branch (the script tolerates either).
	Compressed []bool
}

// ErrNoRepositories is returned when Build is called with an empty repo list.
// The Run reconciler treats this as a permanent Failed state via
// Reason=NoRepos rather than constructing an empty Job.
var ErrNoRepositories = errors.New("sharding: no repositories to assign")

// ErrInvalidBounds is returned when WorkerBounds has nonsensical values
// (Min > Max, Min < 1, Max < 1, ReposPerWorker < 1).
var ErrInvalidBounds = errors.New("sharding: invalid worker bounds")

// Build computes the worker count and shard ConfigMap data for the supplied
// repos and bounds. The repo input is sorted by Slug before assignment so
// the output is stable across runs given the same input set, regardless of
// discovery order.
func Build(repos []Repository, bounds WorkerBounds) (Result, error) {
	if len(repos) == 0 {
		return Result{}, ErrNoRepositories
	}
	if err := validateBounds(bounds); err != nil {
		return Result{}, err
	}

	sorted := make([]Repository, len(repos))
	copy(sorted, repos)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Slug < sorted[j].Slug })

	actual := computeActualWorkers(int32(len(sorted)), bounds)
	shards := assignRoundRobin(sorted, actual)

	data := make(map[string]string, len(shards))
	compressed := make([]bool, len(shards))
	for i, slugs := range shards {
		raw, err := json.Marshal(ShardPayload{Index: i, Total: int(actual), Repos: slugs})
		if err != nil {
			return Result{}, fmt.Errorf("sharding: marshal shard %d: %w", i, err)
		}

		if len(raw) > gzipThresholdBytes {
			encoded, err := gzipBase64(raw)
			if err != nil {
				return Result{}, fmt.Errorf("sharding: gzip shard %d: %w", i, err)
			}
			data[ShardKeyGzip(i)] = encoded
			compressed[i] = true
			continue
		}
		data[ShardKeyJSON(i)] = string(raw)
	}

	return Result{ActualWorkers: actual, Data: data, Compressed: compressed}, nil
}

func validateBounds(b WorkerBounds) error {
	switch {
	case b.MinWorkers < 1:
		return fmt.Errorf("%w: minWorkers=%d must be ≥ 1", ErrInvalidBounds, b.MinWorkers)
	case b.MaxWorkers < 1:
		return fmt.Errorf("%w: maxWorkers=%d must be ≥ 1", ErrInvalidBounds, b.MaxWorkers)
	case b.MinWorkers > b.MaxWorkers:
		return fmt.Errorf("%w: minWorkers=%d > maxWorkers=%d", ErrInvalidBounds, b.MinWorkers, b.MaxWorkers)
	case b.ReposPerWorker < 1:
		return fmt.Errorf("%w: reposPerWorker=%d must be ≥ 1", ErrInvalidBounds, b.ReposPerWorker)
	}
	return nil
}

// computeActualWorkers clamps ceil(len/reposPerWorker) into [min, max].
func computeActualWorkers(repoCount int32, b WorkerBounds) int32 {
	target := (repoCount + b.ReposPerWorker - 1) / b.ReposPerWorker
	switch {
	case target < b.MinWorkers:
		return b.MinWorkers
	case target > b.MaxWorkers:
		return b.MaxWorkers
	default:
		return target
	}
}

// assignRoundRobin distributes repos across n shards. Sorted input means
// the assignment is deterministic across reconciler restarts.
func assignRoundRobin(repos []Repository, n int32) [][]string {
	shards := make([][]string, n)
	for i, r := range repos {
		idx := i % int(n)
		shards[idx] = append(shards[idx], r.Slug)
	}
	return shards
}

// gzipBase64 compresses raw with gzip and base64-encodes the result so the
// payload survives ConfigMap UTF-8 string semantics.
func gzipBase64(raw []byte) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
