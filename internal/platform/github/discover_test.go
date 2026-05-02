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
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"golang.org/x/time/rate"

	"github.com/donaldgifford/renovate-operator/internal/platform"
	ghclient "github.com/donaldgifford/renovate-operator/internal/platform/github"
)

// installationReposServer is a fake GitHub Enterprise endpoint that mocks
// both the ghinstallation token-mint route and the installation-scoped
// repos listing — the two HTTP calls required to exercise the App-auth
// Discover path.
type installationReposServer struct {
	t                *testing.T
	installationID   int64
	pages            map[string]string // page query value → JSON body
	getReposCalls    int
	getReposNextLink func(page int) string
}

func (s *installationReposServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/app/installations/%d/access_tokens", s.installationID):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"ghs_minted","expires_at":"2099-01-01T00:00:00Z"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/installation/repositories":
			s.getReposCalls++
			page := r.URL.Query().Get("page")
			if page == "" {
				page = "1"
			}
			body, ok := s.pages[page]
			if !ok {
				http.Error(w, "no such page", http.StatusNotFound)
				return
			}
			if next := s.getReposNextLink; next != nil {
				if link := next(intPage(page)); link != "" {
					w.Header().Set("Link", link)
				}
			}
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}
}

func intPage(s string) int {
	switch s {
	case "1", "":
		return 1
	case "2":
		return 2
	case "3":
		return 3
	}
	return 0
}

func newAppClient(t *testing.T, srv *httptest.Server, instID int64) *ghclient.Client {
	t.Helper()
	c, err := ghclient.NewWithApp(
		ghclient.AppAuth{
			AppID:          1,
			InstallationID: instID,
			PEM:            generatePEM(t),
			BaseURL:        srv.URL + "/",
		},
		ghclient.WithRateLimit(rate.Inf, 1),
	)
	if err != nil {
		t.Fatalf("NewWithApp: %v", err)
	}
	return c
}

// TestDiscover_AppAuth_UsesInstallationEndpoint verifies the central INV-0004
// fix: an App-auth Client's Discover hits /installation/repositories (the
// installation-scoped endpoint) and never falls back to the public
// /users/{owner}/repos path. The mock server only serves the installation
// endpoint; if Discover hit the org or user listing, it would 404 and
// surface as ErrNotFound instead of the expected repos.
func TestDiscover_AppAuth_UsesInstallationEndpoint(t *testing.T) {
	t.Parallel()

	const instID int64 = 12345
	body := `{
  "total_count": 2,
  "repositories": [
    {"id":1,"name":"private-a","full_name":"donaldgifford/private-a","default_branch":"main","fork":false,"archived":false,"private":true,"owner":{"login":"donaldgifford"}},
    {"id":2,"name":"private-b","full_name":"donaldgifford/private-b","default_branch":"main","fork":false,"archived":false,"private":true,"owner":{"login":"donaldgifford"}}
  ]
}`
	fake := &installationReposServer{
		t:              t,
		installationID: instID,
		pages:          map[string]string{"1": body},
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newAppClient(t, srv, instID)
	got, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "donaldgifford"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Discover returned %d repos, want 2", len(got))
	}
	slugs := []string{got[0].Slug, got[1].Slug}
	for _, want := range []string{"donaldgifford/private-a", "donaldgifford/private-b"} {
		if !slices.Contains(slugs, want) {
			t.Errorf("missing %q in %v", want, slugs)
		}
	}
	if fake.getReposCalls == 0 {
		t.Error("expected at least one /installation/repositories call")
	}
}

// TestDiscover_AppAuth_FiltersByOwner ensures that if /installation/repositories
// returns repos belonging to multiple owners (rare but possible if a Client is
// reused across owners), Discover narrows to filter.Owner. This is defensive —
// the current call site only uses one Client per Platform — but it's the
// guarantee the docstring promises.
func TestDiscover_AppAuth_FiltersByOwner(t *testing.T) {
	t.Parallel()

	const instID int64 = 99
	body := `{
  "total_count": 3,
  "repositories": [
    {"id":1,"name":"keep","full_name":"donaldgifford/keep","default_branch":"main","owner":{"login":"donaldgifford"}},
    {"id":2,"name":"skip-other-owner","full_name":"someoneelse/skip","default_branch":"main","owner":{"login":"someoneelse"}},
    {"id":3,"name":"keep2","full_name":"donaldgifford/keep2","default_branch":"main","owner":{"login":"donaldgifford"}}
  ]
}`
	fake := &installationReposServer{
		t:              t,
		installationID: instID,
		pages:          map[string]string{"1": body},
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newAppClient(t, srv, instID)
	got, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "donaldgifford"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Discover returned %d repos, want 2 (someoneelse/skip filtered)", len(got))
	}
	for _, r := range got {
		if !strings.HasPrefix(r.Slug, "donaldgifford/") {
			t.Errorf("got %q, expected only donaldgifford/* in result", r.Slug)
		}
	}
}

// TestDiscover_AppAuth_PaginatesInstallationRepos exercises the pagination
// loop — Apps.ListRepos honors `page=` query just like the org/user listings,
// and the loop must keep walking while resp.NextPage > 0.
func TestDiscover_AppAuth_PaginatesInstallationRepos(t *testing.T) {
	t.Parallel()

	const instID int64 = 77
	pageOne := `{"total_count":3,"repositories":[
    {"id":1,"full_name":"donaldgifford/p1","default_branch":"main","owner":{"login":"donaldgifford"}},
    {"id":2,"full_name":"donaldgifford/p2","default_branch":"main","owner":{"login":"donaldgifford"}}
  ]}`
	pageTwo := `{"total_count":3,"repositories":[
    {"id":3,"full_name":"donaldgifford/p3","default_branch":"main","owner":{"login":"donaldgifford"}}
  ]}`
	fake := &installationReposServer{
		t:              t,
		installationID: instID,
		pages:          map[string]string{"1": pageOne, "2": pageTwo},
		getReposNextLink: func(page int) string {
			if page == 1 {
				return `<http://x/?page=2>; rel="next"`
			}
			return ""
		},
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newAppClient(t, srv, instID)
	got, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "donaldgifford"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Discover returned %d repos, want 3", len(got))
	}
	if fake.getReposCalls != 2 {
		t.Errorf("expected 2 /installation/repositories calls, got %d", fake.getReposCalls)
	}
}
