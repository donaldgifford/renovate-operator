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

package forgejo

import (
	"context"
	"fmt"
	"path"

	"code.gitea.io/sdk/gitea"

	"github.com/donaldgifford/renovate-operator/internal/platform"
)

const discoverPageSize = 50

// Discover lists repos via /orgs/{owner}/repos and falls back to
// /users/{owner}/repos on 404 (Forgejo personal accounts).
func (c *Client) Discover(ctx context.Context, filter platform.DiscoveryFilter) ([]platform.Repository, error) {
	if filter.Owner == "" {
		return nil, fmt.Errorf("forgejo: DiscoveryFilter.Owner required")
	}

	repos, err := c.listOrgRepos(ctx, filter.Owner)
	if err != nil {
		if !isNotFound(err) {
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

func (c *Client) listOrgRepos(ctx context.Context, owner string) ([]*gitea.Repository, error) {
	opt := gitea.ListOrgReposOptions{ListOptions: gitea.ListOptions{PageSize: discoverPageSize}}
	var all []*gitea.Repository
	for page := 1; ; page++ {
		opt.Page = page
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		repos, resp, err := c.gitea.ListOrgRepos(owner, opt)
		if err != nil {
			return nil, classifyErr(resp, err)
		}
		all = append(all, repos...)
		if len(repos) < discoverPageSize {
			break
		}
	}
	return all, nil
}

func (c *Client) listUserRepos(ctx context.Context, owner string) ([]*gitea.Repository, error) {
	opt := gitea.ListReposOptions{ListOptions: gitea.ListOptions{PageSize: discoverPageSize}}
	var all []*gitea.Repository
	for page := 1; ; page++ {
		opt.Page = page
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		repos, resp, err := c.gitea.ListUserRepos(owner, opt)
		if err != nil {
			return nil, classifyErr(resp, err)
		}
		all = append(all, repos...)
		if len(repos) < discoverPageSize {
			break
		}
	}
	return all, nil
}

func matchesFilter(r *gitea.Repository, filter platform.DiscoveryFilter) bool {
	if filter.SkipArchived && r.Archived {
		return false
	}
	if filter.SkipForks && r.Fork {
		return false
	}
	// Forgejo doesn't expose topics on the repo struct in older SDKs; we
	// silently skip topic filtering when it's empty. Reconcilers requesting
	// topics on a Forgejo platform should expect "no filter" semantics.
	if len(filter.Patterns) > 0 && !anyPatternMatches(r.FullName, filter.Patterns) {
		return false
	}
	return true
}

func anyPatternMatches(fullName string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, fullName); err == nil && ok {
			return true
		}
	}
	return false
}

func toRepo(r *gitea.Repository) platform.Repository {
	return platform.Repository{
		Slug:          r.FullName,
		DefaultBranch: r.DefaultBranch,
		Archived:      r.Archived,
		Fork:          r.Fork,
	}
}
