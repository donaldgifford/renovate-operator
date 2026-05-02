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

package jobspec_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/jobspec"
)

func ghPlatform() v1alpha1.RenovatePlatformSpec {
	return v1alpha1.RenovatePlatformSpec{
		PlatformType:  v1alpha1.PlatformTypeGitHub,
		BaseURL:       "https://api.github.com",
		PresetRepoRef: "github>donaldgifford/renovate-config",
		RenovateImage: "ghcr.io/renovatebot/renovate:latest",
		Auth: v1alpha1.PlatformAuth{
			GitHubApp: &v1alpha1.GitHubAppAuth{
				AppID:          12345,
				InstallationID: 67890,
				PrivateKeyRef:  v1alpha1.SecretKeyReference{Name: "renovate-github-app", Key: "private-key.pem"},
			},
		},
		RunnerConfig: &runtime.RawExtension{Raw: []byte(`{"binarySource":"install","onboarding":false}`)},
	}
}

func forgejoPlatform() v1alpha1.RenovatePlatformSpec {
	return v1alpha1.RenovatePlatformSpec{
		PlatformType:  v1alpha1.PlatformTypeForgejo,
		BaseURL:       "https://forgejo.example.com/api/v1",
		RenovateImage: "ghcr.io/renovatebot/renovate:latest",
		Auth: v1alpha1.PlatformAuth{
			Token: &v1alpha1.TokenAuth{SecretRef: v1alpha1.SecretKeyReference{Name: "forgejo-token", Key: "token"}},
		},
	}
}

func nightlyScan() v1alpha1.RenovateScanSpec {
	bli := int32(2)
	return v1alpha1.RenovateScanSpec{
		PlatformRef: v1alpha1.LocalObjectReference{Name: "github"},
		Schedule:    "0 2 * * *",
		Workers:     v1alpha1.WorkersSpec{MinWorkers: 1, MaxWorkers: 5, ReposPerWorker: 50, BackoffLimitPerIndex: &bli},
		//nolint:modernize // ptr.To(true) is the only correct form here; new(bool) would yield *bool->false.
		Discovery:               v1alpha1.DiscoverySpec{Autodiscover: ptr.To(true), RequireConfig: ptr.To(true)},
		RenovateConfigOverrides: &runtime.RawExtension{Raw: []byte(`{"labels":["dependencies"],"automerge":false}`)},
	}
}

func ghRun() *v1alpha1.RenovateRun {
	return &v1alpha1.RenovateRun{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-20260428", Namespace: "renovate", UID: types.UID("abc-123")},
		Spec: v1alpha1.RenovateRunSpec{
			ScanRef:          v1alpha1.LocalObjectReference{Name: "nightly"},
			PlatformSnapshot: ghPlatform(),
			ScanSnapshot:     nightlyScan(),
		},
	}
}

func ghCMAndCred() (*corev1.ConfigMap, jobspec.CredentialMount) {
	return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "nightly-20260428-shards", Namespace: "renovate"},
			Data:       map[string]string{"shard-0000.json": `{"index":0,"total":1,"repos":["donaldgifford/server-price-tracker"]}`},
		},
		jobspec.CredentialMount{SecretName: "renovate-creds-nightly-20260428", TokenKey: "access-token"}
}

func happyPathJob(t *testing.T) (*batchv1.Job, *corev1.ConfigMap) {
	t.Helper()
	cm, cred := ghCMAndCred()
	job, err := jobspec.BuildWorkerJob(jobspec.BuildInput{
		Run: ghRun(), ShardConfigMap: cm, ActualWorkers: 4, Credential: cred,
	})
	if err != nil {
		t.Fatalf("BuildWorkerJob err = %v", err)
	}
	return job, cm
}

func TestBuildWorkerJob_GitHub_NameAndOwnership(t *testing.T) {
	t.Parallel()
	job, _ := happyPathJob(t)

	if got, want := job.Name, "nightly-20260428-worker"; got != want {
		t.Errorf("Job.Name = %q, want %q", got, want)
	}
	if job.Namespace != "renovate" {
		t.Errorf("Job.Namespace = %q, want renovate", job.Namespace)
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != "abc-123" {
		t.Errorf("owner ref: %+v", job.OwnerReferences)
	}
	if !*job.OwnerReferences[0].Controller || !*job.OwnerReferences[0].BlockOwnerDeletion {
		t.Error("owner ref should be Controller=true, BlockOwnerDeletion=true")
	}
}

func TestBuildWorkerJob_GitHub_JobSpecKnobs(t *testing.T) {
	t.Parallel()
	job, _ := happyPathJob(t)

	if *job.Spec.CompletionMode != batchv1.IndexedCompletion {
		t.Error("CompletionMode != Indexed")
	}
	if *job.Spec.Parallelism != 4 || *job.Spec.Completions != 4 {
		t.Errorf("parallelism/completions = %d/%d, want 4/4", *job.Spec.Parallelism, *job.Spec.Completions)
	}
	if *job.Spec.BackoffLimit != 0 {
		t.Errorf("BackoffLimit = %d, want 0", *job.Spec.BackoffLimit)
	}
	if job.Spec.BackoffLimitPerIndex == nil || *job.Spec.BackoffLimitPerIndex != 2 {
		t.Errorf("BackoffLimitPerIndex = %v, want 2", job.Spec.BackoffLimitPerIndex)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 7*24*60*60 {
		t.Errorf("TTLSecondsAfterFinished = %v, want 604800", job.Spec.TTLSecondsAfterFinished)
	}
}

func TestBuildWorkerJob_GitHub_LabelsPropagate(t *testing.T) {
	t.Parallel()
	job, _ := happyPathJob(t)

	want := map[string]string{
		jobspec.LabelRun:       "nightly-20260428",
		jobspec.LabelScan:      "nightly",
		jobspec.LabelPlatform:  "github",
		jobspec.LabelManagedBy: jobspec.ManagedByValue,
		jobspec.LabelComponent: jobspec.ComponentWorkerValue,
	}
	for k, v := range want {
		if got := job.Labels[k]; got != v {
			t.Errorf("Job.Labels[%q] = %q, want %q", k, got, v)
		}
		if got := job.Spec.Template.Labels[k]; got != v {
			t.Errorf("Pod.Labels[%q] = %q, want %q", k, got, v)
		}
	}
}

func TestBuildWorkerJob_GitHub_PodAndContainer(t *testing.T) {
	t.Parallel()
	job, cm := happyPathJob(t)

	pod := job.Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want Never", pod.RestartPolicy)
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("Pod.Containers = %d, want 1", len(pod.Containers))
	}
	c := pod.Containers[0]
	if c.Name != "renovate" {
		t.Errorf("container Name = %q, want renovate", c.Name)
	}
	if c.Image != "ghcr.io/renovatebot/renovate:latest" {
		t.Errorf("container Image = %q", c.Image)
	}
	if len(c.Command) != 3 || c.Command[0] != "/bin/sh" || c.Command[1] != "-c" {
		t.Errorf("Command = %+v", c.Command)
	}
	if !strings.Contains(c.Command[2], "JOB_COMPLETION_INDEX") {
		t.Error("Command shell should reference JOB_COMPLETION_INDEX")
	}

	if len(pod.Volumes) != 1 || pod.Volumes[0].ConfigMap == nil {
		t.Fatalf("Volumes = %+v", pod.Volumes)
	}
	if pod.Volumes[0].ConfigMap.Name != cm.Name {
		t.Errorf("Volume.ConfigMap.Name = %q, want %q", pod.Volumes[0].ConfigMap.Name, cm.Name)
	}
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].MountPath != "/etc/shards" {
		t.Errorf("VolumeMounts = %+v", c.VolumeMounts)
	}
}

// TestBuildWorkerJob_PodSecuritySatisfiesRestricted asserts the worker pod
// template carries the four fields required by PodSecurity admission's
// "restricted" profile. Without these, namespaces enforcing
// `pod-security.kubernetes.io/enforce: restricted` reject the worker Job's
// pod and the Run reconciler stalls on Discovering.
func TestBuildWorkerJob_PodSecuritySatisfiesRestricted(t *testing.T) {
	t.Parallel()
	job, _ := happyPathJob(t)
	pod := job.Spec.Template.Spec

	if pod.SecurityContext == nil {
		t.Fatal("Pod.SecurityContext must be set for PodSecurity restricted")
	}
	if pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Errorf("Pod.SecurityContext.RunAsNonRoot = %v, want true", pod.SecurityContext.RunAsNonRoot)
	}
	if pod.SecurityContext.SeccompProfile == nil ||
		pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("Pod.SecurityContext.SeccompProfile = %+v, want type=RuntimeDefault", pod.SecurityContext.SeccompProfile)
	}

	if len(pod.Containers) != 1 || pod.Containers[0].SecurityContext == nil {
		t.Fatal("container SecurityContext must be set for PodSecurity restricted")
	}
	csc := pod.Containers[0].SecurityContext
	if csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
		t.Errorf("Container.SecurityContext.AllowPrivilegeEscalation = %v, want false", csc.AllowPrivilegeEscalation)
	}
	if csc.Capabilities == nil || len(csc.Capabilities.Drop) != 1 || csc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Container.SecurityContext.Capabilities.Drop = %+v, want [ALL]", csc.Capabilities)
	}
}

func TestBuildWorkerJob_GitHub_EnvOrdering(t *testing.T) {
	t.Parallel()

	job, err := jobspec.BuildWorkerJob(jobspec.BuildInput{
		Run: ghRun(), ShardConfigMap: cmFor("a"), ActualWorkers: 1, Credential: jobspec.CredentialMount{SecretName: "s", TokenKey: "access-token"},
	})
	if err != nil {
		t.Fatalf("BuildWorkerJob err = %v", err)
	}

	env := job.Spec.Template.Spec.Containers[0].Env
	got := envNames(env)
	// Expected order: PLATFORM, LOG_LEVEL, LOG_FORMAT, ENDPOINT, AUTH (TOKEN), AUTODISCOVER, REQUIRE_CONFIG, RENOVATE_CONFIG, OTEL_SERVICE_NAME, OTLP_ENDPOINT
	want := []string{
		"RENOVATE_PLATFORM", "LOG_LEVEL", "LOG_FORMAT", "RENOVATE_ENDPOINT",
		"RENOVATE_TOKEN",
		"RENOVATE_AUTODISCOVER", "RENOVATE_REQUIRE_CONFIG",
		"RENOVATE_CONFIG", "OTEL_SERVICE_NAME", "OTEL_EXPORTER_OTLP_ENDPOINT",
	}
	if !equalStrings(got, want) {
		t.Errorf("env order:\n got = %v\nwant = %v", got, want)
	}

	// PRESET prepended into extends within RENOVATE_CONFIG
	cfg := envValue(env, "RENOVATE_CONFIG")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil {
		t.Fatalf("RENOVATE_CONFIG not JSON: %v", err)
	}
	extends, _ := parsed["extends"].([]any)
	if len(extends) != 1 || extends[0] != "github>donaldgifford/renovate-config" {
		t.Errorf("extends = %v, want presetRepoRef prepended", extends)
	}
	// Scan override wins on collision (automerge false)
	if got := parsed["automerge"]; got != false {
		t.Errorf("automerge = %v, want false (scan override)", got)
	}
}

func TestBuildWorkerJob_Forgejo(t *testing.T) {
	t.Parallel()

	run := ghRun()
	run.Spec.PlatformSnapshot = forgejoPlatform()

	job, err := jobspec.BuildWorkerJob(jobspec.BuildInput{
		Run: run, ShardConfigMap: cmFor("a"), ActualWorkers: 1,
		Credential: jobspec.CredentialMount{SecretName: "creds", TokenKey: "token"},
	})
	if err != nil {
		t.Fatalf("BuildWorkerJob err = %v", err)
	}
	env := job.Spec.Template.Spec.Containers[0].Env

	if got := envValue(env, "RENOVATE_PLATFORM"); got != "gitea" {
		t.Errorf("RENOVATE_PLATFORM = %q, want gitea (forgejo speaks gitea API)", got)
	}
	if envValue(env, "RENOVATE_GITHUB_APP_ID") != "" || envValue(env, "RENOVATE_GITHUB_APP_KEY") != "" {
		t.Error("forgejo Job should not carry GitHub App env vars")
	}
	tokenEnv := envEntry(env, "RENOVATE_TOKEN")
	if tokenEnv == nil || tokenEnv.ValueFrom == nil || tokenEnv.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("RENOVATE_TOKEN missing or not SecretKeyRef: %+v", tokenEnv)
	}
	if tokenEnv.ValueFrom.SecretKeyRef.Name != "creds" || tokenEnv.ValueFrom.SecretKeyRef.Key != "token" {
		t.Errorf("RENOVATE_TOKEN SecretKeyRef = %+v", tokenEnv.ValueFrom.SecretKeyRef)
	}
}

// TestBuildWorkerJob_AlwaysSetsRenovateToken covers INV-0003 (post-hypothesis-2):
// both auth modes converge on a single RENOVATE_TOKEN env var sourced from
// the per-Run mirrored Secret's access-token key. The operator mints an
// installation token for App auth and writes the static token through for
// Token auth — the worker doesn't see the upstream PEM/PAT directly.
func TestBuildWorkerJob_AlwaysSetsRenovateToken(t *testing.T) {
	t.Parallel()
	t.Run("github-app", func(t *testing.T) {
		t.Parallel()
		job, _ := happyPathJob(t)
		env := job.Spec.Template.Spec.Containers[0].Env
		assertRenovateTokenSourcedFromAccessToken(t, env, "renovate-creds-nightly-20260428")
	})
	t.Run("forgejo-token", func(t *testing.T) {
		t.Parallel()
		run := ghRun()
		run.Spec.PlatformSnapshot = forgejoPlatform()
		job, err := jobspec.BuildWorkerJob(jobspec.BuildInput{
			Run: run, ShardConfigMap: cmFor("a"), ActualWorkers: 1,
			Credential: jobspec.CredentialMount{SecretName: "creds", TokenKey: "access-token"},
		})
		if err != nil {
			t.Fatalf("BuildWorkerJob err = %v", err)
		}
		env := job.Spec.Template.Spec.Containers[0].Env
		assertRenovateTokenSourcedFromAccessToken(t, env, "creds")
	})
}

// TestBuildWorkerJob_NoLegacyAppEnvVars asserts the post-INV-0003 contract:
// the worker pod never sees RENOVATE_GITHUB_APP_ID / KEY. Those were the
// dead-code env vars from hypothesis 1; now the operator mints a token and
// the worker only consumes RENOVATE_TOKEN.
func TestBuildWorkerJob_NoLegacyAppEnvVars(t *testing.T) {
	t.Parallel()
	job, _ := happyPathJob(t)
	env := job.Spec.Template.Spec.Containers[0].Env
	for _, name := range []string{"RENOVATE_GITHUB_APP_ID", "RENOVATE_GITHUB_APP_KEY", "RENOVATE_AUTODISCOVER_FILTER"} {
		if envValue(env, name) != "" || envEntry(env, name) != nil {
			t.Errorf("%s should not be set on the worker pod (INV-0003)", name)
		}
	}
}

// TestEntrypointShell_AlwaysSetsRenovateRepositories asserts the worker
// entrypoint always exports RENOVATE_REPOSITORIES from the shard JSON. The
// auth-type bifurcation introduced under hypothesis 1 was reverted —
// authentication is supplied via RENOVATE_TOKEN (sourced from the mirrored
// Secret), the same way for both auth modes.
func TestEntrypointShell_AlwaysSetsRenovateRepositories(t *testing.T) {
	t.Parallel()
	shell := jobspec.EntrypointShell
	for _, want := range []string{
		`RENOVATE_REPOSITORIES="$(printf '%s' "$DATA" | jq -c '.repos')"`,
		`export RENOVATE_REPOSITORIES`,
		`exec renovate`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("EntrypointShell missing %q", want)
		}
	}
	for _, banned := range []string{
		`RENOVATE_GITHUB_APP_ID`,
		`RENOVATE_AUTODISCOVER_FILTER`,
	} {
		if strings.Contains(shell, banned) {
			t.Errorf("EntrypointShell still references %q (INV-0003 hypothesis-1 leftover)", banned)
		}
	}
}

// assertRenovateTokenSourcedFromAccessToken verifies the worker's
// RENOVATE_TOKEN env entry is a SecretKeyRef pointing at the named mirrored
// Secret + access-token key.
func assertRenovateTokenSourcedFromAccessToken(t *testing.T, env []corev1.EnvVar, wantSecret string) {
	t.Helper()
	tokenEnv := envEntry(env, "RENOVATE_TOKEN")
	if tokenEnv == nil || tokenEnv.ValueFrom == nil || tokenEnv.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("RENOVATE_TOKEN missing or not SecretKeyRef: %+v", tokenEnv)
	}
	if tokenEnv.ValueFrom.SecretKeyRef.Name != wantSecret {
		t.Errorf("RENOVATE_TOKEN SecretKeyRef.Name = %q, want %q", tokenEnv.ValueFrom.SecretKeyRef.Name, wantSecret)
	}
	if tokenEnv.ValueFrom.SecretKeyRef.Key != "access-token" {
		t.Errorf("RENOVATE_TOKEN SecretKeyRef.Key = %q, want \"access-token\"", tokenEnv.ValueFrom.SecretKeyRef.Key)
	}
}

func TestBuildWorkerJob_ExtraEnvAppendedLast(t *testing.T) {
	t.Parallel()

	scan := nightlyScan()
	scan.ExtraEnv = []corev1.EnvVar{{Name: "CUSTOM", Value: "x"}}
	run := ghRun()
	run.Spec.ScanSnapshot = scan

	job, err := jobspec.BuildWorkerJob(jobspec.BuildInput{
		Run: run, ShardConfigMap: cmFor("a"), ActualWorkers: 1, Credential: jobspec.CredentialMount{SecretName: "s", TokenKey: "access-token"},
	})
	if err != nil {
		t.Fatalf("BuildWorkerJob err = %v", err)
	}
	env := job.Spec.Template.Spec.Containers[0].Env
	last := env[len(env)-1]
	if last.Name != "CUSTOM" || last.Value != "x" {
		t.Errorf("last env = %+v, want CUSTOM=x", last)
	}
}

func TestBuildWorkerJob_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*jobspec.BuildInput)
		wantErr error
	}{
		{"nil-run", func(in *jobspec.BuildInput) { in.Run = nil }, jobspec.ErrNilRun},
		{"nil-cm", func(in *jobspec.BuildInput) { in.ShardConfigMap = nil }, jobspec.ErrNilConfigMap},
		{"zero-workers", func(in *jobspec.BuildInput) { in.ActualWorkers = 0 }, jobspec.ErrInvalidWorker},
		{"no-secret", func(in *jobspec.BuildInput) { in.Credential.SecretName = "" }, jobspec.ErrNoCredential},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := jobspec.BuildInput{
				Run: ghRun(), ShardConfigMap: cmFor("a"), ActualWorkers: 1,
				Credential: jobspec.CredentialMount{SecretName: "s", TokenKey: "access-token"},
			}
			tt.mutate(&in)
			_, err := jobspec.BuildWorkerJob(in)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildWorkerJob_AuthMissingKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   jobspec.BuildInput
	}{
		{"github-no-pem-key", jobspec.BuildInput{
			Run: ghRun(), ShardConfigMap: cmFor("a"), ActualWorkers: 1,
			Credential: jobspec.CredentialMount{SecretName: "s"},
		}},
		{"forgejo-no-token-key", jobspec.BuildInput{
			Run:            func() *v1alpha1.RenovateRun { r := ghRun(); r.Spec.PlatformSnapshot = forgejoPlatform(); return r }(),
			ShardConfigMap: cmFor("a"), ActualWorkers: 1,
			Credential: jobspec.CredentialMount{SecretName: "s"},
		}},
		{"no-auth-set", jobspec.BuildInput{
			Run: func() *v1alpha1.RenovateRun {
				r := ghRun()
				r.Spec.PlatformSnapshot.Auth = v1alpha1.PlatformAuth{}
				return r
			}(),
			ShardConfigMap: cmFor("a"), ActualWorkers: 1,
			Credential: jobspec.CredentialMount{SecretName: "s"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := jobspec.BuildWorkerJob(tt.in); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestJobName_TruncatesLongNames(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 80)
	got := jobspec.JobName(long)
	if !strings.HasSuffix(got, "-worker") {
		t.Errorf("JobName(%q) = %q, missing -worker suffix", long, got)
	}
	if len(got) > 63 {
		t.Errorf("JobName length = %d, must be ≤ 63 (DNS-1123 label)", len(got))
	}
}

func TestBuildWorkerJob_NoOptionalFieldsStillBuilds(t *testing.T) {
	t.Parallel()

	scan := v1alpha1.RenovateScanSpec{
		PlatformRef: v1alpha1.LocalObjectReference{Name: "p"},
		Schedule:    "* * * * *",
		Workers:     v1alpha1.WorkersSpec{MinWorkers: 1, MaxWorkers: 1, ReposPerWorker: 1},
		// Explicit false survives now that the field is *bool — leaving it
		// unset would apply the documented default (true). See INV-0005.
		// new(bool) yields *bool→false, matching ptr.To(false) without the lint nag.
		Discovery: v1alpha1.DiscoverySpec{RequireConfig: new(bool)},
	}
	platform := v1alpha1.RenovatePlatformSpec{
		PlatformType:  v1alpha1.PlatformTypeGitHub,
		RenovateImage: "img:latest",
		Auth: v1alpha1.PlatformAuth{
			GitHubApp: &v1alpha1.GitHubAppAuth{AppID: 1, InstallationID: 1, PrivateKeyRef: v1alpha1.SecretKeyReference{Name: "s"}},
		},
	}
	run := &v1alpha1.RenovateRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "n"},
		Spec: v1alpha1.RenovateRunSpec{
			ScanRef:          v1alpha1.LocalObjectReference{Name: "s"},
			PlatformSnapshot: platform,
			ScanSnapshot:     scan,
		},
	}
	job, err := jobspec.BuildWorkerJob(jobspec.BuildInput{
		Run: run, ShardConfigMap: cmFor("c"), ActualWorkers: 1,
		Credential: jobspec.CredentialMount{SecretName: "s", TokenKey: "access-token"},
	})
	if err != nil {
		t.Fatalf("BuildWorkerJob err = %v", err)
	}
	if envValue(job.Spec.Template.Spec.Containers[0].Env, "RENOVATE_ENDPOINT") != "" {
		t.Error("RENOVATE_ENDPOINT should be omitted when baseURL is empty")
	}
	if envValue(job.Spec.Template.Spec.Containers[0].Env, "RENOVATE_REQUIRE_CONFIG") != "" {
		t.Error("RENOVATE_REQUIRE_CONFIG should be omitted when requireConfig=false")
	}
	if envValue(job.Spec.Template.Spec.Containers[0].Env, "RENOVATE_CONFIG") != "" {
		t.Error("RENOVATE_CONFIG should be omitted when no preset/runner/overrides")
	}
}

// helpers

func cmFor(name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "renovate"}}
}

func envNames(env []corev1.EnvVar) []string {
	out := make([]string, len(env))
	for i, e := range env {
		out[i] = e.Name
	}
	return out
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func envEntry(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// BenchmarkBuildWorkerJob exercises the second half of the Reconcile hot
// path (sharding.Build is the first). Both rows use a fully-realistic
// fixture with a non-trivial RenovateConfig override and PresetRepoRef
// extends-prepend, so JSON marshal/unmarshal and env assembly are
// represented. ActualWorkers=1 and =16 cover the two ends of the
// homelab-typical shard count.
func BenchmarkBuildWorkerJob(b *testing.B) {
	cm, cred := ghCMAndCred()
	run := ghRun()

	for _, w := range []int32{1, 16} {
		b.Run(fmt.Sprintf("workers=%d", w), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := jobspec.BuildWorkerJob(jobspec.BuildInput{
					Run: run, ShardConfigMap: cm, ActualWorkers: w, Credential: cred,
				}); err != nil {
					b.Fatalf("BuildWorkerJob err = %v", err)
				}
			}
		})
	}
}
