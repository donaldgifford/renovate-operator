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

// Package controller hosts the three reconcilers that drive the v0.1.0
// Renovate operator: RenovatePlatform (cluster-scoped credential validation),
// RenovateScan (cron-driven Run scheduling per platform), and RenovateRun
// (state-machine reconciliation through Pending → Discovering → Running →
// {Succeeded, Failed} including shard ConfigMap + worker Job ownership).
//
// Each reconciler exposes a SetupWithManager hook for cmd/main.go to wire it
// into a controller-runtime Manager. The platform-client factory used by the
// run reconciler is pluggable (see DefaultPlatformClientFactory) so tests can
// substitute stubs without touching real GitHub or Forgejo SDKs.
package controller
