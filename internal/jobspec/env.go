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

import (
	"encoding/json"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
)

// Renovate env-var names. Documented in DESIGN-0001 § Job builder env vars.
const (
	envRenovatePlatform     = "RENOVATE_PLATFORM"
	envRenovateEndpoint     = "RENOVATE_ENDPOINT"
	envRenovateAutodiscover = "RENOVATE_AUTODISCOVER"
	envRenovateRequireCfg   = "RENOVATE_REQUIRE_CONFIG"
	envRenovateConfig       = "RENOVATE_CONFIG"
	envRenovateToken        = "RENOVATE_TOKEN" // #nosec G101 -- env var name, not a credential

	envLogLevel  = "LOG_LEVEL"
	envLogFormat = "LOG_FORMAT"

	envOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOTELService  = "OTEL_SERVICE_NAME"

	otelServiceName = "renovate-worker"
)

// renovatePlatformID maps our PlatformType to Renovate's CLI platform string.
// Forgejo speaks Gitea's API, so Renovate calls it "gitea".
func renovatePlatformID(t v1alpha1.PlatformType) string {
	switch t {
	case v1alpha1.PlatformTypeGitHub:
		return "github"
	case v1alpha1.PlatformTypeForgejo:
		return "gitea"
	default:
		return string(t)
	}
}

// buildEnv assembles the worker container's env in the order locked by
// DESIGN-0001 § Job builder. Later entries win on key collision; the slice
// is intentionally flat so a maintainer can read top-to-bottom.
func buildEnv(platform v1alpha1.RenovatePlatformSpec, scan v1alpha1.RenovateScanSpec, cred CredentialMount) ([]corev1.EnvVar, error) {
	out := make([]corev1.EnvVar, 0, 16)

	// 1. Platform-derived
	out = append(out,
		corev1.EnvVar{Name: envRenovatePlatform, Value: renovatePlatformID(platform.PlatformType)},
		corev1.EnvVar{Name: envLogLevel, Value: defaultLogLevel},
		corev1.EnvVar{Name: envLogFormat, Value: defaultLogFormat},
	)
	if platform.BaseURL != "" {
		out = append(out, corev1.EnvVar{Name: envRenovateEndpoint, Value: platform.BaseURL})
	}

	// 2. Auth
	authEnvs, err := buildAuthEnv(platform, cred)
	if err != nil {
		return nil, err
	}
	out = append(out, authEnvs...)

	// 3. Discovery. The operator owns the repo list end-to-end and ships it
	//    via RENOVATE_REPOSITORIES (set by the entrypoint shell from the
	//    shard JSON), so RENOVATE_AUTODISCOVER stays "false" for both auth
	//    types. The operator-minted access token is what makes platform init
	//    work — see INV-0003.
	out = append(out, corev1.EnvVar{Name: envRenovateAutodiscover, Value: "false"})
	if scan.Discovery.RequireConfigEnabled() {
		out = append(out, corev1.EnvVar{Name: envRenovateRequireCfg, Value: "required"})
	}

	// 4. RENOVATE_REPOSITORIES is set by the entrypoint shell from the shard
	//    file at /etc/shards/shard-NNNN.json — the controller never writes it
	//    here.

	// 5. RENOVATE_CONFIG: merged platform + scan overrides, with presetRepoRef
	//    prepended into extends.
	cfg, err := mergeRenovateConfig(platform, scan)
	if err != nil {
		return nil, err
	}
	if cfg != "" {
		out = append(out, corev1.EnvVar{Name: envRenovateConfig, Value: cfg})
	}

	// 6. Trace propagation. OTLP endpoint is wired by the controller from its
	//    own env (downward propagation); the env var is added unconditionally
	//    so workers self-disable when it's empty.
	out = append(out,
		corev1.EnvVar{Name: envOTELService, Value: otelServiceName},
		corev1.EnvVar{
			Name: envOTLPEndpoint,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.annotations['renovate.fartlab.dev/otlp-endpoint']"},
			},
		},
	)

	// 7. Scan extraEnv last (user-supplied overrides win)
	out = append(out, scan.ExtraEnv...)

	return out, nil
}

// buildAuthEnv resolves the worker's credential env. Both auth modes converge
// on RENOVATE_TOKEN sourced from the per-Run mirrored Secret's access-token
// key — the operator mints an installation token for App auth and passes
// through the static token for Token auth (see INV-0003). The platform
// type only matters at controller side (Discovery + token mint).
func buildAuthEnv(platform v1alpha1.RenovatePlatformSpec, cred CredentialMount) ([]corev1.EnvVar, error) {
	if platform.Auth.GitHubApp == nil && platform.Auth.Token == nil {
		return nil, fmt.Errorf("jobspec: PlatformAuth has neither githubApp nor token set")
	}
	if cred.TokenKey == "" {
		return nil, fmt.Errorf("jobspec: CredentialMount.TokenKey is required")
	}
	return []corev1.EnvVar{
		{
			Name: envRenovateToken,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: cred.SecretName},
					Key:                  cred.TokenKey,
				},
			},
		},
	}, nil
}

// mergeRenovateConfig combines platform.runnerConfig and scan.renovateConfigOverrides
// (scan wins on key collision) and prepends platform.presetRepoRef into the
// extends array. Returns the JSON string ready for RENOVATE_CONFIG.
func mergeRenovateConfig(platform v1alpha1.RenovatePlatformSpec, scan v1alpha1.RenovateScanSpec) (string, error) {
	merged := map[string]any{}

	if platform.RunnerConfig != nil && len(platform.RunnerConfig.Raw) > 0 {
		var pcfg map[string]any
		if err := json.Unmarshal(platform.RunnerConfig.Raw, &pcfg); err != nil {
			return "", fmt.Errorf("jobspec: parse platform.runnerConfig: %w", err)
		}
		maps.Copy(merged, pcfg)
	}

	if scan.RenovateConfigOverrides != nil && len(scan.RenovateConfigOverrides.Raw) > 0 {
		var scfg map[string]any
		if err := json.Unmarshal(scan.RenovateConfigOverrides.Raw, &scfg); err != nil {
			return "", fmt.Errorf("jobspec: parse scan.renovateConfigOverrides: %w", err)
		}
		maps.Copy(merged, scfg)
	}

	if platform.PresetRepoRef != "" {
		extends, _ := merged["extends"].([]any)
		merged["extends"] = append([]any{platform.PresetRepoRef}, extends...)
	}

	if len(merged) == 0 {
		return "", nil
	}

	out, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("jobspec: marshal RENOVATE_CONFIG: %w", err)
	}
	return string(out), nil
}
