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

package controller

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	renovatev1alpha1 "github.com/donaldgifford/renovate-operator/api/v1alpha1"
	"github.com/donaldgifford/renovate-operator/internal/conditions"
)

// requeueAfterPlatformError is the requeue cadence when the Platform's
// credential Secret hasn't shown up yet (transient).
const requeueAfterPlatformError = 60 * time.Second

// RenovatePlatformReconciler reconciles a RenovatePlatform object.
//
// The Platform controller resolves the credential Secret in the operator's
// release namespace, validates it can be parsed as the configured auth shape
// (PEM for GitHubApp, opaque token for Token), and surfaces the result on
// the Ready condition.
type RenovatePlatformReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// OperatorNamespace is the release namespace where credential Secrets
	// live. Wired in cmd/main.go from POD_NAMESPACE / a flag.
	OperatorNamespace string
}

// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovateplatforms,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovateplatforms/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=renovate.fartlab.dev,resources=renovateplatforms/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile resolves the platform's credential Secret and sets Ready
// accordingly. It does NOT mint installation tokens or hit the platform's
// API at this stage — that's deferred to the Run controller, which has the
// full snapshotted spec. The Platform controller's job is purely "are the
// credentials present and parseable?"
func (r *RenovatePlatformReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var platform renovatev1alpha1.RenovatePlatform
	if err := r.Get(ctx, req.NamespacedName, &platform); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	result, err := r.reconcile(ctx, &platform)
	if updateErr := r.Status().Update(ctx, &platform); updateErr != nil {
		// Status update conflicts are common; surface as transient.
		if apierrors.IsConflict(updateErr) {
			log.V(1).Info("status conflict, requeueing", "platform", req.Name)
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(updateErr, "status update failed", "platform", req.Name)
		if err == nil {
			err = updateErr
		}
	}
	return result, err
}

// reconcile is the inner loop; it mutates platform.Status but does not
// persist it. The caller wraps with a Status().Update.
func (r *RenovatePlatformReconciler) reconcile(ctx context.Context, platform *renovatev1alpha1.RenovatePlatform) (ctrl.Result, error) {
	platform.Status.ObservedGeneration = platform.Generation

	secretName, dataKey, err := r.resolveAuthRef(platform)
	if err != nil {
		conditions.MarkFalse(&platform.Status.Conditions,
			conditions.TypeReady, conditions.ReasonAuthFailed,
			err.Error(), platform.Generation)
		return ctrl.Result{}, nil
	}

	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: r.OperatorNamespace, Name: secretName}
	if err := r.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			conditions.MarkFalse(&platform.Status.Conditions,
				conditions.TypeReady, conditions.ReasonSecretNotFound,
				fmt.Sprintf("Secret %s/%s not found", key.Namespace, key.Name),
				platform.Generation)
			return ctrl.Result{RequeueAfter: requeueAfterPlatformError}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get secret: %w", err)
	}

	value, ok := secret.Data[dataKey]
	if !ok || len(value) == 0 {
		conditions.MarkFalse(&platform.Status.Conditions,
			conditions.TypeReady, conditions.ReasonKeyMissing,
			fmt.Sprintf("Secret %s/%s missing key %q", key.Namespace, key.Name, dataKey),
			platform.Generation)
		return ctrl.Result{RequeueAfter: requeueAfterPlatformError}, nil
	}

	if err := validateAuthMaterial(platform, value); err != nil {
		conditions.MarkFalse(&platform.Status.Conditions,
			conditions.TypeReady, conditions.ReasonAuthFailed,
			err.Error(), platform.Generation)
		return ctrl.Result{}, nil
	}

	conditions.MarkTrue(&platform.Status.Conditions,
		conditions.TypeReady, conditions.ReasonCredentialsResolved,
		"credentials resolved", platform.Generation)
	return ctrl.Result{}, nil
}

// resolveAuthRef returns (secretName, dataKey) for the configured auth.
// Defaults the key to "private-key.pem" for GitHubApp and "token" for Token
// when the user-supplied SecretKeyReference.Key is empty.
func (r *RenovatePlatformReconciler) resolveAuthRef(p *renovatev1alpha1.RenovatePlatform) (string, string, error) {
	auth := p.Spec.Auth
	switch {
	case auth.GitHubApp != nil:
		ref := auth.GitHubApp.PrivateKeyRef
		if ref.Name == "" {
			return "", "", errors.New("auth.githubApp.privateKeyRef.name required")
		}
		key := ref.Key
		if key == "" {
			key = defaultGitHubAppPEMKey
		}
		return ref.Name, key, nil
	case auth.Token != nil:
		ref := auth.Token.SecretRef
		if ref.Name == "" {
			return "", "", errors.New("auth.token.secretRef.name required")
		}
		key := ref.Key
		if key == "" {
			key = defaultTokenKey
		}
		return ref.Name, key, nil
	default:
		return "", "", errors.New("auth must specify githubApp or token")
	}
}

// validateAuthMaterial parses the Secret data per the configured auth shape.
// For GitHubApp we PEM-decode and ParsePKCS1PrivateKey / ParsePKCS8PrivateKey;
// for Token we just check non-empty (already done by the caller).
func validateAuthMaterial(p *renovatev1alpha1.RenovatePlatform, data []byte) error {
	if p.Spec.Auth.GitHubApp != nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return errors.New("github app private key is not PEM-encoded")
		}
		// Try PKCS1 (old GitHub Apps), then PKCS8 (newer apps).
		if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return nil
		}
		if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			return nil
		}
		return errors.New("github app private key is not parseable as PKCS1 or PKCS8")
	}
	return nil
}

// SetupWithManager wires the controller with watches on Platform + Secret.
// The Secret watch maps any Secret in the operator namespace to the
// Platforms whose auth refs match by name, so a credential rotation
// triggers a Platform reconcile without polling.
func (r *RenovatePlatformReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&renovatev1alpha1.RenovatePlatform{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.platformsForSecret),
			builder.WithPredicates(secretInOperatorNamespacePredicate(r.OperatorNamespace)),
		).
		Named("renovateplatform").
		Complete(r)
}

// platformsForSecret returns reconcile requests for every Platform whose
// auth ref points at the supplied Secret.
func (r *RenovatePlatformReconciler) platformsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}
	if secret.Namespace != r.OperatorNamespace {
		return nil
	}

	var list renovatev1alpha1.RenovatePlatformList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	out := make([]reconcile.Request, 0)
	for _, p := range list.Items {
		name, _, err := r.resolveAuthRef(&p)
		if err != nil || name != secret.Name {
			continue
		}
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Name: p.Name}})
	}
	return out
}
