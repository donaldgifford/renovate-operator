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

package jobspec

// Label keys exported from this package are the canonical identifiers used
// across the operator (Job pods, ConfigMaps, mirrored Secrets, metrics).
// Keep them lowercase per K8s label conventions.
const (
	LabelRun      = "renovate.fartlab.dev/run"
	LabelScan     = "renovate.fartlab.dev/scan"
	LabelPlatform = "renovate.fartlab.dev/platform"

	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelComponent = "app.kubernetes.io/component"

	ManagedByValue       = "renovate-operator"
	ComponentWorkerValue = "worker"
)
