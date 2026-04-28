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

package sharding_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/donaldgifford/renovate-operator/internal/sharding"
)

func reposN(n int) []sharding.Repository {
	repos := make([]sharding.Repository, 0, n)
	for i := range n {
		repos = append(repos, sharding.Repository{Slug: fmt.Sprintf("owner/repo-%05d", i)})
	}
	return repos
}

func TestBuildClampingMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		repos      int
		bounds     sharding.WorkerBounds
		wantActual int32
	}{
		{"single-repo-min-floor", 1, sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 5, ReposPerWorker: 50}, 1},
		{"hits-min-floor", 10, sharding.WorkerBounds{MinWorkers: 3, MaxWorkers: 10, ReposPerWorker: 50}, 3},
		{"target-fits-bounds", 200, sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 10, ReposPerWorker: 50}, 4},
		{"hits-max-ceiling", 1000, sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 5, ReposPerWorker: 50}, 5},
		{"exact-multiple", 100, sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 10, ReposPerWorker: 50}, 2},
		{"repos-per-worker-1", 7, sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 100, ReposPerWorker: 1}, 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sharding.Build(reposN(tt.repos), tt.bounds)
			if err != nil {
				t.Fatalf("Build err = %v", err)
			}
			if got.ActualWorkers != tt.wantActual {
				t.Errorf("ActualWorkers = %d, want %d", got.ActualWorkers, tt.wantActual)
			}
			if len(got.Data) != int(tt.wantActual) {
				t.Errorf("len(Data) = %d, want %d", len(got.Data), tt.wantActual)
			}
		})
	}
}

func TestBuildIsStableAcrossInputOrder(t *testing.T) {
	t.Parallel()

	bounds := sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 4, ReposPerWorker: 25}

	in := reposN(100)
	first, err := sharding.Build(in, bounds)
	if err != nil {
		t.Fatalf("Build err = %v", err)
	}

	// shuffle the input order and rebuild
	shuffled := make([]sharding.Repository, len(in))
	for i, r := range in {
		shuffled[(i*7)%len(in)] = r
	}
	second, err := sharding.Build(shuffled, bounds)
	if err != nil {
		t.Fatalf("Build err = %v", err)
	}

	if !reflect.DeepEqual(first.Data, second.Data) {
		t.Errorf("shuffled input produced different shards; sort guard is broken")
	}
}

func TestBuildAssignsEveryRepoExactlyOnce(t *testing.T) {
	t.Parallel()

	repos := reposN(207)
	bounds := sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 5, ReposPerWorker: 50}

	got, err := sharding.Build(repos, bounds)
	if err != nil {
		t.Fatalf("Build err = %v", err)
	}

	seen := make(map[string]int, len(repos))
	for i := int32(0); i < got.ActualWorkers; i++ {
		key := sharding.ShardKeyJSON(int(i))
		raw, ok := got.Data[key]
		if !ok {
			t.Fatalf("missing shard key %q", key)
		}
		var p sharding.ShardPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("unmarshal shard %d: %v", i, err)
		}
		for _, slug := range p.Repos {
			seen[slug]++
		}
	}

	if len(seen) != len(repos) {
		t.Errorf("seen %d unique repos, want %d", len(seen), len(repos))
	}
	for slug, count := range seen {
		if count != 1 {
			t.Errorf("repo %q appeared %d times, want 1", slug, count)
		}
	}
}

func TestBuildGzipPathTriggersAboveThreshold(t *testing.T) {
	t.Parallel()

	// Make slug strings ~3 KiB each so a single shard easily exceeds 900 KiB.
	const slugSize = 3 * 1024
	const repoCount = 400
	bigRepos := make([]sharding.Repository, repoCount)
	pad := strings.Repeat("p", slugSize)
	for i := range repoCount {
		bigRepos[i] = sharding.Repository{Slug: fmt.Sprintf("owner/%s-%05d", pad, i)}
	}

	got, err := sharding.Build(bigRepos, sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 1, ReposPerWorker: 1000})
	if err != nil {
		t.Fatalf("Build err = %v", err)
	}
	if got.ActualWorkers != 1 {
		t.Fatalf("ActualWorkers = %d, want 1 (single big shard)", got.ActualWorkers)
	}

	if !got.Compressed[0] {
		t.Fatal("expected shard 0 to be compressed (size > 900 KiB)")
	}
	if _, ok := got.Data[sharding.ShardKeyGzip(0)]; !ok {
		t.Fatalf("expected key %q present, got keys %+v", sharding.ShardKeyGzip(0), keys(got.Data))
	}

	// Round-trip the gzip+base64 payload to confirm it decodes to a valid ShardPayload.
	encoded := got.Data[sharding.ShardKeyGzip(0)]
	zipped, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	var p sharding.ShardPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.Repos) != repoCount {
		t.Errorf("unmarshalled shard has %d repos, want %d", len(p.Repos), repoCount)
	}
}

func TestBuildBelowThresholdStaysUncompressed(t *testing.T) {
	t.Parallel()

	got, err := sharding.Build(reposN(50), sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 1, ReposPerWorker: 1000})
	if err != nil {
		t.Fatalf("Build err = %v", err)
	}
	if got.Compressed[0] {
		t.Error("small shard should not be compressed")
	}
	if _, ok := got.Data[sharding.ShardKeyJSON(0)]; !ok {
		t.Errorf("expected uncompressed key %q", sharding.ShardKeyJSON(0))
	}
}

func TestBuildErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repos   []sharding.Repository
		bounds  sharding.WorkerBounds
		wantErr error
	}{
		{"empty-repos", nil, sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 1, ReposPerWorker: 1}, sharding.ErrNoRepositories},
		{"min-zero", reposN(1), sharding.WorkerBounds{MinWorkers: 0, MaxWorkers: 1, ReposPerWorker: 1}, sharding.ErrInvalidBounds},
		{"max-zero", reposN(1), sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 0, ReposPerWorker: 1}, sharding.ErrInvalidBounds},
		{"min-gt-max", reposN(1), sharding.WorkerBounds{MinWorkers: 5, MaxWorkers: 1, ReposPerWorker: 1}, sharding.ErrInvalidBounds},
		{"reposPerWorker-zero", reposN(1), sharding.WorkerBounds{MinWorkers: 1, MaxWorkers: 1, ReposPerWorker: 0}, sharding.ErrInvalidBounds},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := sharding.Build(tt.repos, tt.bounds)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Build err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestShardKeyHelpers(t *testing.T) {
	t.Parallel()

	if got, want := sharding.ShardKeyJSON(7), "shard-0007.json"; got != want {
		t.Errorf("ShardKeyJSON(7) = %q, want %q", got, want)
	}
	if got, want := sharding.ShardKeyGzip(7), "shard-0007.json.gz"; got != want {
		t.Errorf("ShardKeyGzip(7) = %q, want %q", got, want)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
