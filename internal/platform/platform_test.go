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

package platform_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/donaldgifford/renovate-operator/internal/platform"
)

func TestConfigPaths_OrderAndCoverage(t *testing.T) {
	t.Parallel()

	want := []string{
		"renovate.json",
		".renovaterc",
		".renovaterc.json",
		".github/renovate.json",
		".gitlab/renovate.json",
	}
	if len(platform.ConfigPaths) != len(want) {
		t.Fatalf("ConfigPaths len = %d, want %d", len(platform.ConfigPaths), len(want))
	}
	for i, p := range platform.ConfigPaths {
		if p != want[i] {
			t.Errorf("ConfigPaths[%d] = %q, want %q", i, p, want[i])
		}
	}
}

func TestRateLimitedError_WrapsTransient(t *testing.T) {
	t.Parallel()

	err := &platform.RateLimitedError{RetryAfter: 30 * time.Second, Cause: errors.New("429")}
	if !errors.Is(err, platform.ErrTransient) {
		t.Error("RateLimitedError should be ErrTransient")
	}
	if !strings.Contains(err.Error(), "30s") {
		t.Errorf("Error() = %q, want retry-after duration", err.Error())
	}

	zero := &platform.RateLimitedError{}
	if zero.Error() == "" || strings.Contains(zero.Error(), "0s") {
		t.Errorf("Error() with zero RetryAfter = %q, should omit duration", zero.Error())
	}
}

func TestErrorSentinelsAreDistinct(t *testing.T) {
	t.Parallel()

	if errors.Is(platform.ErrTransient, platform.ErrPermanent) {
		t.Error("ErrTransient and ErrPermanent must be distinct")
	}
	if !errors.Is(platform.ErrUnauthorized, platform.ErrUnauthorized) {
		t.Error("errors.Is must match itself")
	}
}
