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

package github

import (
	"context"
	"fmt"
	"path"
	"slices"

	gogithub "github.com/google/go-github/v62/github"

	"github.com/donaldgifford/renovate-operator/internal/platform"
)

const discoverPageSize = 100

// Discover lists every repo in filter.Owner that survives the supplied
// filters. v0.1.0 uses /orgs/{owner}/repos paginated; if that 404s the
// caller is likely a user, so we fall back to /users/{user}/repos.
func (c *Client) Discover(ctx context.Context, filter platform.DiscoveryFilter) ([]platform.Repository, error) {
	if filter.Owner == "" {
		return nil, fmt.Errorf("github: DiscoveryFilter.Owner required")
	}

	repos, err := c.listOrgRepos(ctx, filter.Owner)
	if err != nil {
		// /orgs/{owner}/repos 404s for personal accounts; fall back.
		var notFound bool
		// errors.Is matches platform.ErrNotFound via our classifyErr wrapper.
		if isNotFound(err) {
			notFound = true
		}
		if !notFound {
			return nil, err
		}
		repos, err = c.listUserRepos(ctx, filter.Owner)
		if err != nil {
			return nil, err
		}
	}

	out := make([]platform.Repository, 0, len(repos))
	for _, r := range repos {
		if !matchesFilter(r, filter) {
			continue
		}
		out = append(out, toRepo(r))
	}
	return out, nil
}

func (c *Client) listOrgRepos(ctx context.Context, owner string) ([]*gogithub.Repository, error) {
	opt := &gogithub.RepositoryListByOrgOptions{ListOptions: gogithub.ListOptions{PerPage: discoverPageSize}}
	var all []*gogithub.Repository
	for {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		page, resp, err := c.gh.Repositories.ListByOrg(ctx, owner, opt)
		if err != nil {
			return nil, classifyErr(resp, err)
		}
		all = append(all, page...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return all, nil
}

func (c *Client) listUserRepos(ctx context.Context, owner string) ([]*gogithub.Repository, error) {
	opt := &gogithub.RepositoryListByUserOptions{ListOptions: gogithub.ListOptions{PerPage: discoverPageSize}}
	var all []*gogithub.Repository
	for {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		page, resp, err := c.gh.Repositories.ListByUser(ctx, owner, opt)
		if err != nil {
			return nil, classifyErr(resp, err)
		}
		all = append(all, page...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return all, nil
}

func matchesFilter(r *gogithub.Repository, filter platform.DiscoveryFilter) bool {
	if filter.SkipArchived && r.GetArchived() {
		return false
	}
	if filter.SkipForks && r.GetFork() {
		return false
	}
	if len(filter.Topics) > 0 && !anyTopicMatches(r.Topics, filter.Topics) {
		return false
	}
	if len(filter.Patterns) > 0 && !anyPatternMatches(r.GetFullName(), filter.Patterns) {
		return false
	}
	return true
}

func anyTopicMatches(have, want []string) bool {
	for _, t := range want {
		if slices.Contains(have, t) {
			return true
		}
	}
	return false
}

// anyPatternMatches returns true when fullName matches any glob in patterns.
// Renovate's autodiscover globs use fnmatch; path.Match handles the same
// "owner/*" / "owner/prefix-*" shapes for our purposes.
func anyPatternMatches(fullName string, patterns []string) bool {
	for _, p := range patterns {
		ok, err := path.Match(p, fullName)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func toRepo(r *gogithub.Repository) platform.Repository {
	return platform.Repository{
		Slug:          r.GetFullName(),
		DefaultBranch: r.GetDefaultBranch(),
		Archived:      r.GetArchived(),
		Fork:          r.GetFork(),
		Topics:        append([]string(nil), r.Topics...),
	}
}
