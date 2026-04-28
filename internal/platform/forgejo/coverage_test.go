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

package forgejo_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"

	"github.com/donaldgifford/renovate-operator/internal/platform"
	"github.com/donaldgifford/renovate-operator/internal/platform/forgejo"
)

func TestNew_WithHTTPClientOption(t *testing.T) {
	t.Parallel()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/api/v1/version" {
			_, _ = w.Write([]byte(`{"version":"10.0.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	hc := &http.Client{Transport: http.DefaultTransport}
	c, err := forgejo.New(
		forgejo.Auth{BaseURL: srv.URL, Token: "fake"},
		forgejo.WithRateLimit(rate.Inf, 1),
		forgejo.WithHTTPClient(hc),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("client = nil")
	}
	if hits == 0 {
		t.Error("expected forgejo SDK to ping /api/v1/version using the injected HTTP client")
	}
}

func TestNew_TrailingSlashStripped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"10.0.0"}`))
	}))
	t.Cleanup(srv.Close)

	// Trailing slash is harmless; gitea.NewClient still constructs cleanly.
	if _, err := forgejo.New(
		forgejo.Auth{BaseURL: srv.URL + "/", Token: "fake"},
		forgejo.WithRateLimit(rate.Inf, 1),
	); err != nil {
		t.Fatalf("New with trailing slash: %v", err)
	}
}

func TestDiscover_NotFoundClassified(t *testing.T) {
	t.Parallel()
	handlers := map[string]http.HandlerFunc{
		// Both org and user lookups return 404 — the discoverer should
		// surface ErrNotFound.
		"GET /api/v1/orgs/missing/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no such org", http.StatusNotFound)
		},
		"GET /api/v1/users/missing/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no such user", http.StatusNotFound)
		},
	}
	c := newClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "missing"})
	if err == nil {
		t.Fatal("err = nil, want non-nil")
	}
	if !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDiscover_ServerErrorIsTransient(t *testing.T) {
	t.Parallel()
	handlers := map[string]http.HandlerFunc{
		"GET /api/v1/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusServiceUnavailable)
		},
	}
	c := newClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if err == nil {
		t.Fatal("err = nil")
	}
	if !errors.Is(err, platform.ErrTransient) {
		t.Errorf("err = %v, want ErrTransient", err)
	}
}

func TestDiscover_RateLimitedClassified(t *testing.T) {
	t.Parallel()
	handlers := map[string]http.HandlerFunc{
		"GET /api/v1/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "slow down", http.StatusTooManyRequests)
		},
	}
	c := newClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if err == nil {
		t.Fatal("err = nil")
	}
	var rle *platform.RateLimitedError
	if !errors.As(err, &rle) {
		t.Errorf("err = %v, want *RateLimitedError", err)
	}
}

func TestDiscover_PatternFilterMatches(t *testing.T) {
	t.Parallel()
	handlers := map[string]http.HandlerFunc{
		"GET /api/v1/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[
				{"id":1,"full_name":"o/keep","fork":false,"archived":false,"default_branch":"main"},
				{"id":2,"full_name":"o/drop","fork":false,"archived":false,"default_branch":"main"}
			]`))
		},
	}
	c := newClient(t, handlers)
	repos, err := c.Discover(context.Background(),
		platform.DiscoveryFilter{Owner: "o", Patterns: []string{"o/keep"}})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(repos) != 1 || repos[0].Slug != "o/keep" {
		t.Errorf("repos = %+v, want [o/keep]", repos)
	}
}

func TestHasRenovateConfig_InvalidSlug(t *testing.T) {
	t.Parallel()
	c := newClient(t, map[string]http.HandlerFunc{})
	_, err := c.HasRenovateConfig(context.Background(), platform.Repository{Slug: "no-slash"})
	if err == nil {
		t.Error("err = nil, want invalid-slug error")
	}
}

func TestHasRenovateConfig_PermanentErrorPropagates(t *testing.T) {
	t.Parallel()
	handlers := map[string]http.HandlerFunc{
		// First config probe returns 401 — auth failure is permanent and
		// should bubble up.
		"GET /api/v1/repos/o/r/contents/renovate.json": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no", http.StatusUnauthorized)
		},
	}
	c := newClient(t, handlers)
	_, err := c.HasRenovateConfig(context.Background(),
		platform.Repository{Slug: "o/r", DefaultBranch: "main"})
	if err == nil {
		t.Fatal("err = nil")
	}
	if !errors.Is(err, platform.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}
