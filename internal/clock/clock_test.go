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

package clock_test

import (
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/donaldgifford/renovate-operator/internal/clock"
)

func TestRealClockReportsCurrentWallTime(t *testing.T) {
	t.Parallel()

	before := time.Now()
	got := clock.RealClock().Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Fatalf("RealClock().Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestFakeClockSatisfiesInterface(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, time.April, 28, 12, 0, 0, 0, time.UTC)
	var c clock.Clock = clocktesting.NewFakeClock(want)

	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("FakeClock.Now() = %v, want %v", got, want)
	}
}
