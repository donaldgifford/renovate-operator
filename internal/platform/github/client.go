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

// Package github implements platform.Client against the GitHub REST API
// using google/go-github/v62 + bradleyfalzon/ghinstallation/v2 for App auth.
// PAT auth is also supported (TokenAuth → Bearer header) for simple cases,
// but the v0.1.0 homelab path uses GitHub App installation tokens.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	ghinstallation "github.com/bradleyfalzon/ghinstallation/v2"
	gogithub "github.com/google/go-github/v62/github"
	"golang.org/x/time/rate"

	"github.com/donaldgifford/renovate-operator/internal/platform"
)

// Default rate-limit budget per IMPL-0001 Q2: 4500 req/hr sustained, 100 burst,
// per GitHub App installation. Conservative against the 5000/hr primary cap.
const (
	defaultRateLimit rate.Limit = 4500.0 / 3600.0 // ~1.25 req/sec
	defaultRateBurst            = 100
)

// AppAuth is the inputs needed to mint an installation token. The PEM bytes
// come from the mirrored Secret in the Run's namespace.
type AppAuth struct {
	AppID          int64
	InstallationID int64
	PEM            []byte
	BaseURL        string // optional GHES URL; empty means api.github.com
}

// TokenAuth is for personal-access-token / fine-grained-token flows.
type TokenAuth struct {
	Token   string
	BaseURL string // optional GHES URL
}

// ClientOption tunes the constructed Client.
type ClientOption func(*Client)

// WithRateLimit overrides the default 4500/hr rate limit. Used in tests to
// remove the rate limiter (rate.Inf, large burst) so VCR fixtures replay fast.
func WithRateLimit(r rate.Limit, burst int) ClientOption {
	return func(c *Client) {
		c.limiter = rate.NewLimiter(r, burst)
	}
}

// WithHTTPClient injects a custom *http.Client. Used in tests to wire up the
// VCR transport.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = httpClient
	}
}

// WithBaseURL overrides the GHES base URL after construction.
func WithBaseURL(base string) ClientOption {
	return func(c *Client) {
		c.baseURL = base
	}
}

// Client implements platform.Client against GitHub.
type Client struct {
	gh         *gogithub.Client
	httpClient *http.Client
	baseURL    string
	limiter    *rate.Limiter

	// appTransport is set when the Client was constructed via NewWithApp.
	// MintAccessToken pulls a fresh installation token from it.
	appTransport *ghinstallation.Transport

	// staticToken is set when the Client was constructed via NewWithToken
	// (PAT auth). MintAccessToken returns it unchanged.
	staticToken string
}

// NewWithApp constructs a Client backed by GitHub App installation auth.
func NewWithApp(auth AppAuth, opts ...ClientOption) (*Client, error) {
	if auth.AppID == 0 || auth.InstallationID == 0 {
		return nil, fmt.Errorf("github: AppID and InstallationID required")
	}
	if len(auth.PEM) == 0 {
		return nil, fmt.Errorf("github: PEM required")
	}

	c := &Client{
		baseURL: auth.BaseURL,
		limiter: rate.NewLimiter(defaultRateLimit, defaultRateBurst),
	}
	for _, opt := range opts {
		opt(c)
	}

	transport := http.DefaultTransport
	if c.httpClient != nil && c.httpClient.Transport != nil {
		transport = c.httpClient.Transport
	}
	itr, err := ghinstallation.New(transport, auth.AppID, auth.InstallationID, auth.PEM)
	if err != nil {
		return nil, fmt.Errorf("github: install transport: %w", err)
	}
	if c.baseURL != "" {
		itr.BaseURL = c.baseURL
	}

	gh, err := buildGoGitHubClient(&http.Client{Transport: itr}, c.baseURL)
	if err != nil {
		return nil, err
	}
	c.gh = gh
	c.appTransport = itr
	return c, nil
}

// NewWithToken constructs a Client backed by token auth.
func NewWithToken(auth TokenAuth, opts ...ClientOption) (*Client, error) {
	if auth.Token == "" {
		return nil, fmt.Errorf("github: token required")
	}

	c := &Client{
		baseURL: auth.BaseURL,
		limiter: rate.NewLimiter(defaultRateLimit, defaultRateBurst),
	}
	for _, opt := range opts {
		opt(c)
	}

	httpClient := c.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Transport: &tokenTransport{token: auth.Token, base: http.DefaultTransport}}
	} else {
		httpClient.Transport = &tokenTransport{token: auth.Token, base: httpClient.Transport}
	}

	gh, err := buildGoGitHubClient(httpClient, c.baseURL)
	if err != nil {
		return nil, err
	}
	c.gh = gh
	c.staticToken = auth.Token
	return c, nil
}

// MintAccessToken returns a token usable as RENOVATE_TOKEN. App-auth Clients
// mint a fresh installation token via ghinstallation (TTL ~1h on github.com).
// Token-auth Clients return the configured PAT unchanged with a zero
// expiresAt — PATs don't expire on a fixed schedule. See INV-0003.
func (c *Client) MintAccessToken(ctx context.Context) (string, time.Time, error) {
	if c.appTransport != nil {
		tok, err := c.appTransport.Token(ctx)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("github: mint installation token: %w", err)
		}
		expiresAt, _, err := c.appTransport.Expiry()
		if err != nil {
			return "", time.Time{}, fmt.Errorf("github: install-token expiry: %w", err)
		}
		return tok, expiresAt, nil
	}
	if c.staticToken == "" {
		return "", time.Time{}, fmt.Errorf("github: client has no auth credential")
	}
	return c.staticToken, time.Time{}, nil
}

func buildGoGitHubClient(httpClient *http.Client, baseURL string) (*gogithub.Client, error) {
	gh := gogithub.NewClient(httpClient)
	if baseURL == "" {
		return gh, nil
	}
	out, err := gh.WithEnterpriseURLs(baseURL, baseURL)
	if err != nil {
		return nil, fmt.Errorf("github: GHES base URL: %w", err)
	}
	return out, nil
}

// tokenTransport injects "Authorization: Bearer <token>" on every request.
type tokenTransport struct {
	token string
	base  http.RoundTripper
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req2)
}

// wait blocks until the rate limiter admits one request or ctx is cancelled.
func (c *Client) wait(ctx context.Context) error {
	return c.limiter.Wait(ctx)
}

// classifyErr converts a go-github response/error pair into our error
// sentinels so the reconciler can distinguish transient from permanent
// without importing go-github types.
func classifyErr(resp *gogithub.Response, err error) error {
	if err == nil {
		return nil
	}
	var rateErr *gogithub.RateLimitError
	var abuseErr *gogithub.AbuseRateLimitError
	switch {
	case errors.As(err, &rateErr):
		retry := 0
		if !rateErr.Rate.Reset.IsZero() {
			// duration until reset, never negative
			retry = max(0, int(rateErr.Rate.Reset.Unix()))
		}
		return &platform.RateLimitedError{RetryAfter: durationFrom(retry), Cause: err}
	case errors.As(err, &abuseErr):
		var retry = 0
		if abuseErr.RetryAfter != nil {
			retry = int(abuseErr.RetryAfter.Seconds())
		}
		return &platform.RateLimitedError{RetryAfter: durationFrom(retry), Cause: err}
	}
	if resp != nil {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: %v", platform.ErrUnauthorized, err)
		case http.StatusNotFound:
			return fmt.Errorf("%w: %v", platform.ErrNotFound, err)
		case http.StatusTooManyRequests:
			return &platform.RateLimitedError{Cause: err}
		}
		if resp.StatusCode >= 500 {
			return fmt.Errorf("%w: %v", platform.ErrTransient, err)
		}
	}
	return fmt.Errorf("%w: %v", platform.ErrPermanent, err)
}
