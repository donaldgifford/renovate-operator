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

// Package platform defines the small Client interface that the Run
// reconciler uses to enumerate repos and check for Renovate config files.
// Concrete implementations live in internal/platform/github and
// internal/platform/forgejo; reconcilers depend only on the interface.
package platform

import (
	"context"
	"errors"
	"time"
)

// Repository is the discovery result for one repo. Only fields the reconciler
// reads are surfaced; richer metadata stays inside the platform-specific
// client.
type Repository struct {
	// Slug is the platform-qualified path ("owner/repo"). The same string
	// flows into RENOVATE_REPOSITORIES.
	Slug string

	// DefaultBranch is the repo's default branch (e.g., "main"). Used by
	// HasRenovateConfig to know which ref to query.
	DefaultBranch string

	// Archived is true for archived repos. Discovery may filter on this.
	Archived bool

	// Fork is true for forked repos. Discovery may filter on this.
	Fork bool

	// Topics are the repo's topics (GitHub) or labels (Forgejo, where applicable).
	Topics []string
}

// DiscoveryFilter is the platform-agnostic shape of a Scan's spec.discovery.
// The Run reconciler translates v1alpha1.DiscoverySpec into a DiscoveryFilter
// before calling Client.Discover.
type DiscoveryFilter struct {
	// Patterns are Renovate-style autodiscover globs ("owner/*", "owner/prefix-*").
	// Empty means no filter.
	Patterns []string

	// Topics restricts to repos with at least one matching topic. GitHub only;
	// Forgejo silently ignores.
	Topics []string

	// SkipForks drops fork repos.
	SkipForks bool

	// SkipArchived drops archived repos.
	SkipArchived bool

	// Owner is the org/user to enumerate against (e.g., "donaldgifford"). Set
	// once at Run start by the reconciler.
	Owner string
}

// Client is the small surface of platform-side work the reconciler needs.
// Implementations must be safe for concurrent use by multiple goroutines —
// HasRenovateConfig is dispatched concurrently by an errgroup during
// discovery.
type Client interface {
	// Discover enumerates the repos that survive filter/topics/skipForks/
	// skipArchived. The result is unsorted; the sharding builder sorts before
	// assigning shards.
	Discover(ctx context.Context, filter DiscoveryFilter) ([]Repository, error)

	// HasRenovateConfig returns true when the repo has at least one of:
	// renovate.json, .renovaterc, .renovaterc.json, .github/renovate.json,
	// .gitlab/renovate.json on its default branch.
	HasRenovateConfig(ctx context.Context, repo Repository) (bool, error)

	// MintAccessToken returns a token that can authenticate to the platform's
	// git API. For GitHub App auth this is a freshly-minted installation
	// token (~1h TTL on github.com); for token auth it's the static
	// configured token returned unchanged with a zero expiresAt (PATs and
	// Forgejo tokens don't expire on a fixed schedule).
	//
	// The Run reconciler calls this once per Run, writes the result into
	// the per-Run mirrored Secret as `access-token`, and the worker pod
	// consumes it as RENOVATE_TOKEN. See INV-0003.
	MintAccessToken(ctx context.Context) (token string, expiresAt time.Time, err error)
}

// ConfigPaths is the ordered list of files HasRenovateConfig probes. First
// 200 OK wins. Exposed so tests can match the same set without duplicating
// the constants.
var ConfigPaths = []string{
	"renovate.json",
	".renovaterc",
	".renovaterc.json",
	".github/renovate.json",
	".gitlab/renovate.json",
}

// Error sentinels. Reconcilers distinguish transient (worth a retry) from
// permanent (set Ready=False with a clear reason and stop) so they can
// requeue intelligently.
var (
	// ErrTransient wraps any condition that's likely to clear on its own:
	// network blip, primary/secondary rate limit, 5xx. Reconcilers requeue.
	ErrTransient = errors.New("platform: transient error")

	// ErrPermanent wraps a condition that won't fix itself without user
	// intervention: 401/403 (bad credentials), 404 (org doesn't exist),
	// malformed config. Reconcilers surface via condition and stop.
	ErrPermanent = errors.New("platform: permanent error")

	// ErrUnauthorized is a refinement of ErrPermanent for 401/403 responses.
	// Reconcilers map it to Reason=AuthFailed.
	ErrUnauthorized = errors.New("platform: unauthorized")

	// ErrNotFound is a refinement of ErrPermanent for 404 responses on
	// known-good URL shapes (e.g., /orgs/{org}/repos for an org that doesn't exist).
	ErrNotFound = errors.New("platform: not found")
)

// RateLimitedError is returned when the upstream platform asked us to slow
// down. Carries the requested retry interval so the caller can wait the
// suggested duration before retrying.
type RateLimitedError struct {
	// RetryAfter is the duration the platform asked the caller to wait
	// before retrying. Zero means the platform did not suggest a value.
	RetryAfter time.Duration

	// Cause is the underlying error from the platform client (preserved for
	// logging / metrics).
	Cause error
}

// Error implements error. Always wraps ErrTransient so callers using
// errors.Is(err, platform.ErrTransient) match.
func (e *RateLimitedError) Error() string {
	if e.RetryAfter > 0 {
		return "platform: rate limited, retry after " + e.RetryAfter.String()
	}
	return "platform: rate limited"
}

// Unwrap reports ErrTransient so errors.Is(err, ErrTransient) returns true.
func (e *RateLimitedError) Unwrap() error { return ErrTransient }
