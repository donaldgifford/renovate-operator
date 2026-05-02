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

// Package jobspec builds the Indexed Job that runs Renovate workers for a
// RenovateRun. Pure: takes the Run + the shard ConfigMap by reference,
// returns a *batchv1.Job ready for client.Create. No client calls, no
// clock, no mutations to inputs.
package jobspec

import (
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrutil "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
)

// Defaults locked by DESIGN-0001 § Job builder.
const (
	defaultLogLevel  = "info"
	defaultLogFormat = "json"

	// defaultJobTTLSeconds retains owned Jobs for seven days post-terminal.
	defaultJobTTLSeconds int32 = 7 * 24 * 60 * 60

	workerContainerName = "renovate"
	shardVolumeName     = "shards"

	jobNameSuffix = "-worker"
	maxJobNameLen = apierrutil.DNS1123LabelMaxLength
)

// CredentialMount carries pointers into the mirrored credential Secret in
// the Run's namespace. The Run reconciler mints an access token via
// platform.Client.MintAccessToken, writes it into the mirrored Secret, and
// the worker mounts it as RENOVATE_TOKEN. See INV-0003.
type CredentialMount struct {
	// SecretName is the mirrored Secret in the Run's namespace.
	SecretName string

	// TokenKey is the data key holding the access token (typically
	// credentials.MirrorAccessTokenKey, "access-token").
	TokenKey string
}

// BuildInput is the closed set of inputs for the builder.
type BuildInput struct {
	// Run owns the snapshots of Platform and Scan plus the OwnerReference target.
	Run *v1alpha1.RenovateRun

	// ShardConfigMap is the already-created ConfigMap holding shard-NNNN.json keys.
	ShardConfigMap *corev1.ConfigMap

	// ActualWorkers is the result of sharding.Build; must equal the number of
	// shard keys in ShardConfigMap.
	ActualWorkers int32

	// Credential is the mirrored Secret + key resolution.
	Credential CredentialMount
}

// Build errors.
var (
	ErrNilRun        = errors.New("jobspec: nil Run")
	ErrNilConfigMap  = errors.New("jobspec: nil shard ConfigMap")
	ErrInvalidWorker = errors.New("jobspec: actualWorkers must be ≥ 1")
	ErrNoCredential  = errors.New("jobspec: credential mount must have a SecretName")
)

// BuildWorkerJob materializes the Indexed Job for a Run. The returned Job
// has no UID/ResourceVersion; the caller is expected to client.Create it.
func BuildWorkerJob(in BuildInput) (*batchv1.Job, error) {
	if in.Run == nil {
		return nil, ErrNilRun
	}
	if in.ShardConfigMap == nil {
		return nil, ErrNilConfigMap
	}
	if in.ActualWorkers < 1 {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidWorker, in.ActualWorkers)
	}
	if in.Credential.SecretName == "" {
		return nil, ErrNoCredential
	}

	platform := in.Run.Spec.PlatformSnapshot
	scan := in.Run.Spec.ScanSnapshot

	envs, err := buildEnv(platform, scan, in.Credential)
	if err != nil {
		return nil, err
	}

	labels := WorkerLabels(in.Run)
	jobName := JobName(in.Run.Name)
	completionMode := batchv1.IndexedCompletion
	ttl := defaultJobTTLSeconds

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobName,
			Namespace:       in.Run.Namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{ownerRefFor(in.Run)},
		},
		Spec: batchv1.JobSpec{
			CompletionMode: &completionMode,
			//nolint:modernize // ptr.To wraps a runtime value, not a type literal.
			Parallelism: ptr.To(in.ActualWorkers),
			//nolint:modernize // ptr.To wraps a runtime value, not a type literal.
			Completions:             ptr.To(in.ActualWorkers),
			BackoffLimit:            ptr.To[int32](0),
			BackoffLimitPerIndex:    scan.Workers.BackoffLimitPerIndex,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					// Pod-level SecurityContext satisfies PodSecurity admission's
					// "restricted" profile alongside the container-level fields below.
					SecurityContext: &corev1.PodSecurityContext{
						//nolint:modernize // new(bool) returns *false; we need *true.
						RunAsNonRoot: ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: shardVolumeName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: in.ShardConfigMap.Name},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    workerContainerName,
							Image:   platform.RenovateImage,
							Command: []string{"/bin/sh", "-c", EntrypointShell},
							Env:     envs,
							VolumeMounts: []corev1.VolumeMount{
								{Name: shardVolumeName, MountPath: ShardMountPath, ReadOnly: true},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: new(bool),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
						},
					},
				},
			},
		},
	}
	if scan.Resources != nil {
		job.Spec.Template.Spec.Containers[0].Resources = *scan.Resources
	}

	return job, nil
}

// JobName returns the worker Job name for a Run, truncated to fit the
// DNS-1123 label length budget after the "-worker" suffix.
func JobName(runName string) string {
	suffix := jobNameSuffix
	max := maxJobNameLen - len(suffix)
	if len(runName) > max {
		runName = runName[:max]
	}
	return runName + suffix
}

// WorkerLabels returns the canonical label set for worker pods, the Job,
// and any sibling ConfigMap.
func WorkerLabels(run *v1alpha1.RenovateRun) map[string]string {
	scan := run.Spec.ScanRef.Name
	platform := run.Spec.PlatformSnapshot.PlatformType
	return map[string]string{
		LabelRun:       run.Name,
		LabelScan:      scan,
		LabelPlatform:  string(platform),
		LabelManagedBy: ManagedByValue,
		LabelComponent: ComponentWorkerValue,
	}
}

func ownerRefFor(run *v1alpha1.RenovateRun) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{
		APIVersion:         v1alpha1.GroupVersion.String(),
		Kind:               "RenovateRun",
		Name:               run.Name,
		UID:                run.UID,
		Controller:         &yes,
		BlockOwnerDeletion: &yes,
	}
}
