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

type fakeServer struct {
	handlers map[string]http.HandlerFunc
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	if h, ok := s.handlers[key]; ok {
		h(w, r)
		return
	}
	http.NotFound(w, r)
}

func newClient(t *testing.T, handlers map[string]http.HandlerFunc) *forgejo.Client {
	t.Helper()
	// Forgejo SDK probes /api/v1/version on construction; supply a default
	// version response so tests don't have to.
	if _, ok := handlers["GET /api/v1/version"]; !ok {
		handlers["GET /api/v1/version"] = func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"version":"10.0.0"}`))
		}
	}
	srv := httptest.NewServer(&fakeServer{handlers: handlers})
	t.Cleanup(srv.Close)

	c, err := forgejo.New(
		forgejo.Auth{BaseURL: srv.URL, Token: "fake"},
		forgejo.WithRateLimit(rate.Inf, 1),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	return c
}

func TestDiscover_OrgHappyPath(t *testing.T) {
	t.Parallel()

	body := `[
  {"id":1,"name":"a","full_name":"o/a","default_branch":"main","fork":false,"archived":false},
  {"id":2,"name":"b","full_name":"o/b","default_branch":"main","fork":true,"archived":false}
]`
	handlers := map[string]http.HandlerFunc{
		"GET /api/v1/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		},
	}
	c := newClient(t, handlers)

	got, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o", SkipForks: true})
	if err != nil {
		t.Fatalf("Discover err = %v", err)
	}
	if len(got) != 1 || got[0].Slug != "o/a" {
		t.Errorf("Discover = %+v, want o/a", got)
	}
}

func TestDiscover_FallsBackToUser(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v1/orgs/u/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Not Found", http.StatusNotFound)
		},
		"GET /api/v1/users/u/repos": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"id":1,"full_name":"u/r","default_branch":"main"}]`))
		},
	}
	c := newClient(t, handlers)

	got, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "u"})
	if err != nil {
		t.Fatalf("Discover err = %v", err)
	}
	if len(got) != 1 || got[0].Slug != "u/r" {
		t.Errorf("Discover = %+v, want u/r", got)
	}
}

func TestHasRenovateConfig_FirstHit(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v1/repos/o/r/contents/renovate.json": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Not Found", http.StatusNotFound)
		},
		"GET /api/v1/repos/o/r/contents/.renovaterc": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"name":".renovaterc","type":"file","path":".renovaterc","content":"e30K","encoding":"base64"}`))
		},
	}
	c := newClient(t, handlers)

	got, err := c.HasRenovateConfig(context.Background(), platform.Repository{Slug: "o/r", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("HasRenovateConfig err = %v", err)
	}
	if !got {
		t.Error("expected true; .renovaterc should match")
	}
}

func TestHasRenovateConfig_AllMissing(t *testing.T) {
	t.Parallel()
	c := newClient(t, map[string]http.HandlerFunc{})
	got, err := c.HasRenovateConfig(context.Background(), platform.Repository{Slug: "o/r", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("HasRenovateConfig err = %v", err)
	}
	if got {
		t.Error("expected false")
	}
}

func TestUnauthorizedClassifies(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v1/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		},
	}
	c := newClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if !errors.Is(err, platform.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestNewValidatesInputs(t *testing.T) {
	t.Parallel()
	if _, err := forgejo.New(forgejo.Auth{}); err == nil {
		t.Error("New{} should error")
	}
	if _, err := forgejo.New(forgejo.Auth{BaseURL: "x"}); err == nil {
		t.Error("New without token should error")
	}
}

// TestMintAccessToken_ReturnsStaticToken covers the Forgejo path: there's
// no token-minting on Forgejo (the configured PAT is what we use), so
// MintAccessToken returns the static token unchanged with a zero expiresAt.
func TestMintAccessToken_ReturnsStaticToken(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"10.0.0"}`))
	}))
	t.Cleanup(srv.Close)

	c, err := forgejo.New(forgejo.Auth{BaseURL: srv.URL, Token: "fjt-static"})
	if err != nil {
		t.Fatalf("forgejo.New: %v", err)
	}
	tok, expiresAt, err := c.MintAccessToken(context.Background())
	if err != nil {
		t.Fatalf("MintAccessToken: %v", err)
	}
	if tok != "fjt-static" {
		t.Errorf("token = %q, want fjt-static", tok)
	}
	if !expiresAt.IsZero() {
		t.Errorf("expiresAt = %v, want zero", expiresAt)
	}
}
