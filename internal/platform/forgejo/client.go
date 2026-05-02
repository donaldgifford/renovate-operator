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

// Package forgejo implements platform.Client against a Forgejo (or Gitea)
// instance using code.gitea.io/sdk/gitea — the SDKs are interchangeable
// since Forgejo is API-compatible.
package forgejo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"code.gitea.io/sdk/gitea"
	"golang.org/x/time/rate"

	"github.com/donaldgifford/renovate-operator/internal/platform"
)

// Default rate-limit budget per IMPL-0001 Q2: 30 req/sec for Forgejo.
const (
	defaultRateLimit rate.Limit = 30.0
	defaultRateBurst int        = 30
)

// Auth carries the Forgejo token plus the API base URL.
type Auth struct {
	BaseURL string // required, e.g., https://forgejo.fartlab.dev
	Token   string
}

// ClientOption tunes the constructed Client.
type ClientOption func(*Client)

// WithRateLimit overrides the default rate limit for tests.
func WithRateLimit(r rate.Limit, burst int) ClientOption {
	return func(c *Client) {
		c.limiter = rate.NewLimiter(r, burst)
	}
}

// WithHTTPClient injects a custom *http.Client for tests.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = httpClient
	}
}

// Client implements platform.Client against Forgejo.
type Client struct {
	gitea      *gitea.Client
	httpClient *http.Client
	limiter    *rate.Limiter

	// staticToken is the configured Forgejo token. MintAccessToken returns
	// it unchanged; Forgejo tokens don't expire on a fixed schedule.
	staticToken string
}

// New constructs a Client.
func New(auth Auth, opts ...ClientOption) (*Client, error) {
	if auth.BaseURL == "" {
		return nil, fmt.Errorf("forgejo: BaseURL required")
	}
	if auth.Token == "" {
		return nil, fmt.Errorf("forgejo: Token required")
	}

	c := &Client{
		limiter: rate.NewLimiter(defaultRateLimit, defaultRateBurst),
	}
	for _, opt := range opts {
		opt(c)
	}

	clientOpts := []gitea.ClientOption{gitea.SetToken(auth.Token)}
	if c.httpClient != nil {
		clientOpts = append(clientOpts, gitea.SetHTTPClient(c.httpClient))
	}
	gc, err := gitea.NewClient(strings.TrimRight(auth.BaseURL, "/"), clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("forgejo: gitea.NewClient: %w", err)
	}
	c.gitea = gc
	c.staticToken = auth.Token
	return c, nil
}

// MintAccessToken returns the static Forgejo token unchanged. Forgejo tokens
// don't expire on a fixed schedule, so expiresAt is the zero time. See
// INV-0003.
func (c *Client) MintAccessToken(_ context.Context) (string, time.Time, error) {
	return c.staticToken, time.Time{}, nil
}

func (c *Client) wait(ctx context.Context) error {
	return c.limiter.Wait(ctx)
}

// classifyErr maps gitea SDK errors onto our sentinels.
func classifyErr(resp *gitea.Response, err error) error {
	if err == nil {
		return nil
	}
	if resp != nil && resp.Response != nil {
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
	// Network-level errors (no response) are transient.
	if resp == nil || resp.Response == nil {
		return fmt.Errorf("%w: %v", platform.ErrTransient, err)
	}
	return fmt.Errorf("%w: %v", platform.ErrPermanent, err)
}

// isNotFound reports whether the error came from a 404 response.
func isNotFound(err error) bool {
	return errors.Is(err, platform.ErrNotFound)
}
