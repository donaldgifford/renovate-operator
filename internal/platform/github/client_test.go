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

package github_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"golang.org/x/time/rate"

	ghclient "github.com/donaldgifford/renovate-operator/internal/platform/github"

	"github.com/donaldgifford/renovate-operator/internal/platform"
)

// fakeServer wires up a minimal subset of GitHub's REST API so we can drive
// the client end-to-end without a network. Each path responds based on the
// handlers map; missing paths return 404.
type fakeServer struct {
	t        *testing.T
	handlers map[string]http.HandlerFunc
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	h, ok := s.handlers[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h(w, r)
}

func newFakeClient(t *testing.T, handlers map[string]http.HandlerFunc) *ghclient.Client {
	t.Helper()
	srv := httptest.NewServer(&fakeServer{t: t, handlers: handlers})
	t.Cleanup(srv.Close)

	c, err := ghclient.NewWithToken(
		ghclient.TokenAuth{Token: "fake-token", BaseURL: srv.URL + "/"},
		ghclient.WithRateLimit(rate.Inf, 1),
	)
	if err != nil {
		t.Fatalf("NewWithToken err = %v", err)
	}
	return c
}

func TestDiscover_HappyPath_PaginatedOrg(t *testing.T) {
	t.Parallel()

	page1 := strings.NewReplacer("__NEXT__", "1").Replace(`[
  {"id":1,"name":"a","full_name":"o/a","default_branch":"main","fork":false,"archived":false,"topics":["go"]},
  {"id":2,"name":"b","full_name":"o/b","default_branch":"main","fork":true,"archived":false,"topics":[]}
]`)
	page2 := `[{"id":3,"name":"c","full_name":"o/c","default_branch":"main","fork":false,"archived":true,"topics":[]}]`

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, r *http.Request) {
			page := r.URL.Query().Get("page")
			switch page {
			case "", "1":
				w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/orgs/o/repos?page=2>; rel="next"`, "http://x"))
				_, _ = w.Write([]byte(page1))
			case "2":
				_, _ = w.Write([]byte(page2))
			default:
				http.NotFound(w, r)
			}
		},
	}
	c := newFakeClient(t, handlers)

	got, err := c.Discover(context.Background(), platform.DiscoveryFilter{
		Owner: "o", SkipForks: true, SkipArchived: true,
	})
	if err != nil {
		t.Fatalf("Discover err = %v", err)
	}
	if len(got) != 1 || got[0].Slug != "o/a" {
		t.Errorf("Discover = %+v, want exactly o/a (forks and archived dropped)", got)
	}
	if got[0].DefaultBranch != "main" || len(got[0].Topics) != 1 || got[0].Topics[0] != "go" {
		t.Errorf("repo metadata not propagated: %+v", got[0])
	}
}

func TestDiscover_FallsBackToUserOn404(t *testing.T) {
	t.Parallel()

	body := `[{"id":1,"name":"r","full_name":"u/r","default_branch":"main","fork":false,"archived":false}]`
	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/u/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Not Found", http.StatusNotFound)
		},
		"GET /api/v3/users/u/repos": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		},
	}
	c := newFakeClient(t, handlers)

	got, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "u"})
	if err != nil {
		t.Fatalf("Discover err = %v", err)
	}
	if len(got) != 1 || got[0].Slug != "u/r" {
		t.Errorf("Discover = %+v, want u/r", got)
	}
}

func TestDiscover_TopicAndPatternFilter(t *testing.T) {
	t.Parallel()

	body := `[
  {"id":1,"full_name":"o/keep","default_branch":"main","topics":["dependencies"]},
  {"id":2,"full_name":"o/skip","default_branch":"main","topics":["frontend"]},
  {"id":3,"full_name":"o/prefix-yes","default_branch":"main","topics":["dependencies"]}
]`
	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		},
	}
	c := newFakeClient(t, handlers)

	got, err := c.Discover(context.Background(), platform.DiscoveryFilter{
		Owner: "o", Topics: []string{"dependencies"}, Patterns: []string{"o/prefix-*", "o/keep"},
	})
	if err != nil {
		t.Fatalf("Discover err = %v", err)
	}
	slugs := make([]string, 0, len(got))
	for _, r := range got {
		slugs = append(slugs, r.Slug)
	}
	wantContains := []string{"o/keep", "o/prefix-yes"}
	for _, w := range wantContains {
		if !slices.Contains(slugs, w) {
			t.Errorf("missing %q in %v", w, slugs)
		}
	}
	if slices.Contains(slugs, "o/skip") {
		t.Errorf("o/skip should be filtered out by topic; got %v", slugs)
	}
}

func TestHasRenovateConfig_FirstHitWins(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		// renovate.json 404, .renovaterc 404, .renovaterc.json hits.
		"GET /api/v3/repos/o/r/contents/renovate.json": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Not Found", http.StatusNotFound)
		},
		"GET /api/v3/repos/o/r/contents/.renovaterc": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Not Found", http.StatusNotFound)
		},
		"GET /api/v3/repos/o/r/contents/.renovaterc.json": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"name":".renovaterc.json","type":"file","content":"e30K","encoding":"base64"}`))
		},
	}
	c := newFakeClient(t, handlers)

	got, err := c.HasRenovateConfig(context.Background(), platform.Repository{Slug: "o/r", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("HasRenovateConfig err = %v", err)
	}
	if !got {
		t.Error("expected true; .renovaterc.json should match")
	}
}

func TestHasRenovateConfig_AllMissing(t *testing.T) {
	t.Parallel()

	c := newFakeClient(t, map[string]http.HandlerFunc{})
	got, err := c.HasRenovateConfig(context.Background(), platform.Repository{Slug: "o/r", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("HasRenovateConfig err = %v", err)
	}
	if got {
		t.Error("expected false; no paths should match")
	}
}

func TestHasRenovateConfig_InvalidSlug(t *testing.T) {
	t.Parallel()
	c := newFakeClient(t, map[string]http.HandlerFunc{})
	if _, err := c.HasRenovateConfig(context.Background(), platform.Repository{Slug: "no-slash"}); err == nil {
		t.Error("expected error on invalid slug")
	}
}

func TestUnauthorizedClassifies(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Bad credentials", http.StatusUnauthorized)
		},
	}
	c := newFakeClient(t, handlers)

	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if !errors.Is(err, platform.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestRateLimitedClassifies(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		},
	}
	c := newFakeClient(t, handlers)

	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	var rl *platform.RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want *RateLimitedError", err)
	}
	if !errors.Is(err, platform.ErrTransient) {
		t.Errorf("RateLimitedError should be ErrTransient, got %v", err)
	}
}

func TestMalformedJSONIsPermanent(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{not valid json[`))
		},
	}
	c := newFakeClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
	if errors.Is(err, platform.ErrTransient) {
		t.Errorf("malformed JSON should NOT be ErrTransient: %v", err)
	}
}

func TestDiscover_FiftyReposPaginated(t *testing.T) {
	t.Parallel()

	// Two pages × 30 + 1 partial page so the pagination loop terminates.
	makePage := func(start, end int) string {
		var sb strings.Builder
		sb.WriteString("[")
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"id":%d,"full_name":"o/repo-%d","default_branch":"main"}`, i, i)
		}
		sb.WriteString("]")
		return sb.String()
	}

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, r *http.Request) {
			page := r.URL.Query().Get("page")
			switch page {
			case "", "1":
				w.Header().Set("Link", `<http://x/?page=2>; rel="next"`)
				_, _ = w.Write([]byte(makePage(0, 30)))
			case "2":
				w.Header().Set("Link", `<http://x/?page=3>; rel="next"`)
				_, _ = w.Write([]byte(makePage(30, 60)))
			case "3":
				_, _ = w.Write([]byte(`[]`))
			default:
				http.NotFound(w, r)
			}
		},
	}
	c := newFakeClient(t, handlers)

	got, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if err != nil {
		t.Fatalf("Discover err = %v", err)
	}
	if len(got) != 60 {
		t.Errorf("Discover returned %d repos, want 60", len(got))
	}
}

func TestServerErrorClassifiesAsTransient(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusBadGateway)
		},
	}
	c := newFakeClient(t, handlers)

	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if !errors.Is(err, platform.ErrTransient) {
		t.Errorf("err = %v, want ErrTransient", err)
	}
}

func TestNewWith_InputValidation(t *testing.T) {
	t.Parallel()

	if _, err := ghclient.NewWithApp(ghclient.AppAuth{}); err == nil {
		t.Error("NewWithApp{} should error on missing inputs")
	}
	if _, err := ghclient.NewWithToken(ghclient.TokenAuth{}); err == nil {
		t.Error("NewWithToken{} should error on missing token")
	}
}

func TestDiscover_RequiresOwner(t *testing.T) {
	t.Parallel()
	c := newFakeClient(t, map[string]http.HandlerFunc{})
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{})
	if err == nil {
		t.Error("expected error for empty Owner")
	}
}

// TestUnexpectedStatusIsPermanent covers the default ErrPermanent
// fall-through in classifyErr — a 4xx that's not 401/403/404/429 lands
// in the catch-all `return ErrPermanent`. 422 is what GitHub uses on
// validation failures.
func TestUnexpectedStatusIsPermanent(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "validation failed", http.StatusUnprocessableEntity)
		},
		"GET /api/v3/users/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "validation failed", http.StatusUnprocessableEntity)
		},
	}
	c := newFakeClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if err == nil {
		t.Fatal("err = nil")
	}
	if !errors.Is(err, platform.ErrPermanent) {
		t.Errorf("err = %v, want ErrPermanent", err)
	}
}

// TestNotFoundClassifies covers the 404 branch in classifyErr when the
// HasRenovateConfig probe lands a 404 on every config filename — which
// is the success path (returns false, nil) and so doesn't hit
// classifyErr's NotFound branch. Use a bare-Discover at a missing user
// to exercise the path: org returns 404 → fallback to user → user 404
// returns ErrNotFound.
func TestNotFoundClassifies(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/missing/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
		"GET /api/v3/users/missing/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
	}
	c := newFakeClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "missing"})
	if err == nil {
		t.Fatal("err = nil")
	}
	if !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestForbiddenClassifies covers the 403 branch alongside the existing
// 401 test. Forbidden is the more common GitHub auth-failure shape
// (e.g., expired tokens, IP allowlists).
func TestForbiddenClassifies(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		},
	}
	c := newFakeClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if !errors.Is(err, platform.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

// TestTypedRateLimitErrorClassifies covers the typed *RateLimitError path
// in classifyErr — go-github constructs this when the response is 403 +
// X-RateLimit-Remaining: 0 + body message "API rate limit exceeded for ".
// The classifier should map it to *platform.RateLimitedError, not the
// generic 403 ErrUnauthorized branch.
func TestTypedRateLimitErrorClassifies(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1700000000")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 1.2.3.4","documentation_url":"https://docs.github.com/v3"}`))
		},
	}
	c := newFakeClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if err == nil {
		t.Fatal("err = nil")
	}
	var rle *platform.RateLimitedError
	if !errors.As(err, &rle) {
		t.Errorf("err = %v, want *RateLimitedError", err)
	}
}

// TestAbuseRateLimitErrorClassifies covers the typed *AbuseRateLimitError
// path. go-github constructs this when status is 403 and the body's
// documentation_url ends with "/v3/#abuse-rate-limits". A Retry-After
// header is optional but exercised here.
func TestAbuseRateLimitErrorClassifies(t *testing.T) {
	t.Parallel()

	handlers := map[string]http.HandlerFunc{
		"GET /api/v3/orgs/o/repos": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"You have triggered an abuse detection mechanism","documentation_url":"https://docs.github.com/v3/#abuse-rate-limits"}`))
		},
	}
	c := newFakeClient(t, handlers)
	_, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "o"})
	if err == nil {
		t.Fatal("err = nil")
	}
	var rle *platform.RateLimitedError
	if !errors.As(err, &rle) {
		t.Errorf("err = %v, want *RateLimitedError", err)
	}
}
