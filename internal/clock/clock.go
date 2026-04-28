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

// Package clock wraps k8s.io/utils/clock so reconcilers and tests share one
// time source. Reconcilers depend on the Clock interface; production wires
// in RealClock, tests wire in a fake from k8s.io/utils/clock/testing.
package clock

import (
	utilclock "k8s.io/utils/clock"
)

// Clock is the subset of utilclock.PassiveClock and utilclock.Clock that the
// reconcilers and builders need. Re-exporting under our own name keeps the
// upstream import out of consumer signatures.
type Clock = utilclock.Clock

// PassiveClock is the read-only subset (no Sleep, no NewTimer); fine for
// builders that only need "what time is it?".
type PassiveClock = utilclock.PassiveClock

// RealClock returns a Clock backed by the operating system's wall clock.
func RealClock() Clock {
	return utilclock.RealClock{}
}
