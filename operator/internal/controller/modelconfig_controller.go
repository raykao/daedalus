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
	"crypto/sha256"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	forgev1alpha1 "github.com/raykao/agent-forge/operator/api/v1alpha1"
)

const (
	// modelConfigHashAnnotation is the annotation set on AgentRuntimes to trigger
	// reconciliation when the referenced ModelConfig's secret hash changes.
	modelConfigHashAnnotation = "forge.agentforge.dev/model-config-hash"

	// mcConditionTypeAvailable indicates the ModelConfig is valid and its
	// referenced secret (if any) is accessible.
	mcConditionTypeAvailable = "Available"

	// mcConditionTypeDegraded indicates the ModelConfig is in a degraded state
	// (e.g., referenced secret not found).
	mcConditionTypeDegraded = "Degraded"
)

// ModelConfigReconciler reconciles a ModelConfig object.
// It tracks SHA-256 hashes of referenced secrets and triggers downstream
// AgentRuntime reconciliation when credentials rotate.
type ModelConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=forge.agentforge.dev,resources=modelconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=forge.agentforge.dev,resources=modelconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=forge.agentforge.dev,resources=modelconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=forge.agentforge.dev,resources=agentruntimes,verbs=get;list;watch;update;patch

// Reconcile watches ModelConfig CRs and their referenced Secrets. When a
// secret's content changes (detected via SHA-256 hash), it propagates the
// new hash to downstream AgentRuntimes via annotation, triggering their
// reconciliation for credential rotation.
func (r *ModelConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the ModelConfig CR.
	var mc forgev1alpha1.ModelConfig
	if err := r.Get(ctx, req.NamespacedName, &mc); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("ModelConfig not found; likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching ModelConfig: %w", err)
	}

	// 2. Resolve secret reference and compute hash.
	secretHash, err := r.resolveSecretHash(ctx, &mc)
	if err != nil {
		// Secret not found: set Degraded condition, clear Available.
		apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
			Type:               mcConditionTypeDegraded,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: mc.Generation,
			Reason:             "SecretNotFound",
			Message:            fmt.Sprintf("Referenced secret not found: %v", err),
		})
		apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
			Type:               mcConditionTypeAvailable,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: mc.Generation,
			Reason:             "SecretNotFound",
			Message:            "ModelConfig is unavailable because the referenced secret was not found.",
		})
		mc.Status.ObservedGeneration = mc.Generation
		if statusErr := r.Status().Update(ctx, &mc); statusErr != nil {
			log.Error(statusErr, "failed to update ModelConfig status (degraded)")
			return ctrl.Result{}, statusErr
		}
		// Requeue to retry secret resolution.
		return ctrl.Result{RequeueAfter: 30_000_000_000}, nil // 30s
	}

	// 3. Check if the secret hash changed.
	hashChanged := secretHash != mc.Status.SecretHash
	if hashChanged && secretHash != "" {
		log.Info("Secret hash changed, propagating to downstream AgentRuntimes",
			"oldHash", mc.Status.SecretHash, "newHash", secretHash)
	}

	// 4. Update status: secretHash, conditions, observedGeneration.
	mc.Status.SecretHash = secretHash
	mc.Status.ObservedGeneration = mc.Generation

	// Clear Degraded, set Available.
	apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
		Type:               mcConditionTypeDegraded,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: mc.Generation,
		Reason:             "Resolved",
		Message:            "All references resolved successfully.",
	})
	apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
		Type:               mcConditionTypeAvailable,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: mc.Generation,
		Reason:             "ConfigValid",
		Message:            "ModelConfig is available and all references are resolved.",
	})

	if err := r.Status().Update(ctx, &mc); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating ModelConfig status: %w", err)
	}

	// 5. If hash changed, annotate downstream AgentRuntimes.
	if hashChanged {
		if err := r.propagateHashToAgentRuntimes(ctx, &mc, secretHash); err != nil {
			return ctrl.Result{}, fmt.Errorf("propagating hash to AgentRuntimes: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

// resolveSecretHash reads the referenced secret (if any) and returns a SHA-256
// hash of its data. Returns empty string if no secret reference is configured.
func (r *ModelConfigReconciler) resolveSecretHash(ctx context.Context, mc *forgev1alpha1.ModelConfig) (string, error) {
	ref := secretRefFromModelConfig(mc)
	if ref == nil {
		// No secret reference configured; nothing to hash.
		return "", nil
	}

	// Look up the secret in the ModelConfig's namespace.
	var secret corev1.Secret
	nn := types.NamespacedName{
		Namespace: mc.Namespace,
		Name:      ref.Name,
	}
	if err := r.Get(ctx, nn, &secret); err != nil {
		return "", err
	}

	return hashSecretData(secret.Data), nil
}

// secretRefFromModelConfig extracts the SecretKeyRef from the ModelConfig's
// apiKey field, or nil if no secret reference is configured.
func secretRefFromModelConfig(mc *forgev1alpha1.ModelConfig) *forgev1alpha1.KeySelector {
	if mc.Spec.APIKey == nil {
		return nil
	}
	if mc.Spec.APIKey.ValueFrom == nil {
		return nil
	}
	return mc.Spec.APIKey.ValueFrom.SecretKeyRef
}

// hashSecretData computes a deterministic SHA-256 hash over the secret's data
// map. Keys are sorted to ensure consistent ordering.
func hashSecretData(data map[string][]byte) string {
	if len(data) == 0 {
		return ""
	}

	h := sha256.New()

	// Sort keys for deterministic hashing.
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("="))
		h.Write(data[k])
		h.Write([]byte("\n"))
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// propagateHashToAgentRuntimes finds all AgentRuntimes that reference this
// ModelConfig and updates the model-config-hash annotation to trigger their
// reconciliation.
func (r *ModelConfigReconciler) propagateHashToAgentRuntimes(
	ctx context.Context,
	mc *forgev1alpha1.ModelConfig,
	hash string,
) error {
	log := logf.FromContext(ctx)

	// List all AgentRuntimes in the ModelConfig's namespace.
	var runtimeList forgev1alpha1.AgentRuntimeList
	if err := r.List(ctx, &runtimeList); err != nil {
		return fmt.Errorf("listing AgentRuntimes: %w", err)
	}

	for i := range runtimeList.Items {
		rt := &runtimeList.Items[i]
		if !referencesModelConfig(rt, mc) {
			continue
		}

		// Update annotation to trigger AgentRuntime reconciliation.
		if rt.Annotations == nil {
			rt.Annotations = make(map[string]string)
		}
		if rt.Annotations[modelConfigHashAnnotation] == hash {
			continue // already up to date
		}

		rt.Annotations[modelConfigHashAnnotation] = hash
		if err := r.Update(ctx, rt); err != nil {
			log.Error(err, "failed to annotate AgentRuntime",
				"agentRuntime", rt.Name, "namespace", rt.Namespace)
			return err
		}
		log.Info("Annotated AgentRuntime with updated model config hash",
			"agentRuntime", rt.Name, "hash", hash)
	}

	return nil
}

// referencesModelConfig checks whether an AgentRuntime's modelConfigRef points
// to the given ModelConfig.
func referencesModelConfig(rt *forgev1alpha1.AgentRuntime, mc *forgev1alpha1.ModelConfig) bool {
	ref := rt.Spec.ModelConfigRef
	if ref == nil {
		return false
	}
	if ref.Name != mc.Name {
		return false
	}
	// Check namespace: empty means same namespace as the AgentRuntime.
	refNS := ref.Namespace
	if refNS == "" {
		refNS = rt.Namespace
	}
	return refNS == mc.Namespace
}

// SetupWithManager sets up the controller with the Manager.
// It watches ModelConfig CRs and also watches Secrets, mapping them back to
// the ModelConfigs that reference them.
func (r *ModelConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&forgev1alpha1.ModelConfig{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapSecretToModelConfigs)).
		Named("modelconfig").
		Complete(r)
}

// mapSecretToModelConfigs maps a Secret change to the ModelConfigs that
// reference it via spec.apiKey.valueFrom.secretKeyRef.
func (r *ModelConfigReconciler) mapSecretToModelConfigs(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	// List all ModelConfigs in the secret's namespace.
	var mcList forgev1alpha1.ModelConfigList
	if err := r.List(ctx, &mcList, client.InNamespace(secret.Namespace)); err != nil {
		logf.FromContext(ctx).Error(err, "failed to list ModelConfigs for secret mapping",
			"secret", secret.Name, "namespace", secret.Namespace)
		return nil
	}

	var requests []reconcile.Request
	for i := range mcList.Items {
		mc := &mcList.Items[i]
		ref := secretRefFromModelConfig(mc)
		if ref != nil && ref.Name == secret.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: mc.Namespace,
					Name:      mc.Name,
				},
			})
		}
	}

	return requests
}
