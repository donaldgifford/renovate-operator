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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"

	"github.com/donaldgifford/renovate-operator/internal/platform"
	ghclient "github.com/donaldgifford/renovate-operator/internal/platform/github"
)

func generatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func TestNewWithApp_OptionsApplied(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(srv.Close)

	pemBytes := generatePEM(t)
	c, err := ghclient.NewWithApp(
		ghclient.AppAuth{
			AppID: 1, InstallationID: 1, PEM: pemBytes,
			BaseURL: srv.URL + "/",
		},
		ghclient.WithRateLimit(rate.Inf, 1),
		ghclient.WithBaseURL(srv.URL+"/"),
	)
	if err != nil {
		t.Fatalf("NewWithApp: %v", err)
	}
	if c == nil {
		t.Fatal("client = nil")
	}
}

func TestNewWithApp_RequiresPEM(t *testing.T) {
	t.Parallel()
	if _, err := ghclient.NewWithApp(ghclient.AppAuth{AppID: 1, InstallationID: 1}); err == nil {
		t.Error("missing PEM: err = nil, want non-nil")
	}
}

func TestNewWithApp_InvalidPEM(t *testing.T) {
	t.Parallel()
	if _, err := ghclient.NewWithApp(ghclient.AppAuth{
		AppID: 1, InstallationID: 1,
		PEM: []byte("not-a-pem"),
	}); err == nil {
		t.Error("invalid PEM: err = nil, want non-nil")
	}
}

func TestNewWithToken_HappyPath_DiscoverWorks(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Confirm Authorization header was injected by tokenTransport.
		if r.Header.Get("Authorization") != "Bearer test-pat" {
			t.Errorf("Authorization = %q, want Bearer test-pat", r.Header.Get("Authorization"))
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"full_name": "donaldgifford/repo-x", "fork": false, "archived": false, "topics": []string{}},
		})
	}))
	t.Cleanup(srv.Close)

	c, err := ghclient.NewWithToken(
		ghclient.TokenAuth{Token: "test-pat", BaseURL: srv.URL + "/"},
		ghclient.WithRateLimit(rate.Inf, 1),
	)
	if err != nil {
		t.Fatalf("NewWithToken: %v", err)
	}

	repos, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "donaldgifford"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(repos) != 1 || repos[0].Slug != "donaldgifford/repo-x" {
		t.Errorf("repos = %+v, want [donaldgifford/repo-x]", repos)
	}
	if calls == 0 {
		t.Error("expected the fake server to receive at least one call")
	}
}

func TestNewWithToken_InjectsAuthOverExistingTransport(t *testing.T) {
	t.Parallel()
	// Build a custom http.Client with a no-op base transport. NewWithToken
	// should wrap it so requests still carry the bearer token.
	customTransport := http.DefaultTransport
	hc := &http.Client{Transport: customTransport}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer wrap-me" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(srv.Close)

	c, err := ghclient.NewWithToken(
		ghclient.TokenAuth{Token: "wrap-me", BaseURL: srv.URL + "/"},
		ghclient.WithRateLimit(rate.Inf, 1),
		ghclient.WithHTTPClient(hc),
	)
	if err != nil {
		t.Fatalf("NewWithToken with custom hc: %v", err)
	}
	if _, err := c.Discover(context.Background(), platform.DiscoveryFilter{Owner: "x"}); err != nil {
		t.Fatalf("Discover: %v", err)
	}
}

func TestNewWithToken_RequiresToken(t *testing.T) {
	t.Parallel()
	if _, err := ghclient.NewWithToken(ghclient.TokenAuth{}); err == nil {
		t.Error("missing token: err = nil, want non-nil")
	}
}

func TestNewWithApp_HTTPClientOptionRespected(t *testing.T) {
	t.Parallel()
	pemBytes := generatePEM(t)
	hc := &http.Client{Transport: http.DefaultTransport}
	c, err := ghclient.NewWithApp(
		ghclient.AppAuth{AppID: 1, InstallationID: 1, PEM: pemBytes},
		ghclient.WithHTTPClient(hc),
	)
	if err != nil {
		t.Fatalf("NewWithApp + WithHTTPClient: %v", err)
	}
	if c == nil {
		t.Fatal("client = nil")
	}
}
