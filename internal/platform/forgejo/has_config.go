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
	"errors"
	"fmt"
	"strings"

	"github.com/donaldgifford/renovate-operator/internal/platform"
)

// HasRenovateConfig probes the canonical config paths via Forgejo's contents
// API; first 200 wins, 404s fall through.
func (c *Client) HasRenovateConfig(ctx context.Context, repo platform.Repository) (bool, error) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return false, fmt.Errorf("forgejo: invalid slug %q", repo.Slug)
	}

	for _, p := range platform.ConfigPaths {
		if err := c.wait(ctx); err != nil {
			return false, err
		}
		_, resp, err := c.gitea.GetContents(owner, name, repo.DefaultBranch, p)
		if err == nil {
			return true, nil
		}
		classified := classifyErr(resp, err)
		if errors.Is(classified, platform.ErrNotFound) {
			continue
		}
		return false, classified
	}
	return false, nil
}

func splitSlug(slug string) (string, string, bool) {
	idx := strings.IndexByte(slug, '/')
	if idx <= 0 || idx == len(slug)-1 {
		return "", "", false
	}
	return slug[:idx], slug[idx+1:], true
}
