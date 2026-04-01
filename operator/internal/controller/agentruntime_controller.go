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
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	forgev1alpha1 "github.com/raykao/agent-forge/operator/api/v1alpha1"
)

const (
	// proxyImage is the sidecar proxy image injected into ScaledJob pods.
	proxyImage = "ghcr.io/raykao/agent-forge-proxy:latest"

	// configMapSuffix is appended to the AgentRuntime name for the ConfigMap.
	configMapSuffix = "-agent-registry"

	// serviceSuffix is appended to the AgentRuntime name for the Service.
	serviceSuffix = "-a2a"

	// conditionTypeAvailable indicates the runtime is available and serving.
	conditionTypeAvailable = "Available"

	// conditionTypeProgressing indicates the runtime is being reconciled.
	conditionTypeProgressing = "Progressing"

	// conditionTypeDegraded indicates the runtime is in a degraded state.
	conditionTypeDegraded = "Degraded"
)

// scaledJobGVR is the GroupVersionResource for KEDA ScaledJob.
var scaledJobGVR = schema.GroupVersionResource{
	Group:    "keda.sh",
	Version:  "v1alpha1",
	Resource: "scaledjobs",
}

// scaledJobGVK is the GroupVersionKind for KEDA ScaledJob.
var scaledJobGVK = schema.GroupVersionKind{
	Group:   "keda.sh",
	Version: "v1alpha1",
	Kind:    "ScaledJob",
}

// agentRegistryEntry is the JSON structure stored in the agent registry ConfigMap.
type agentRegistryEntry struct {
	Card              interface{} `json:"card"`
	QueueSubject      string      `json:"queueSubject"`
	Runtime           string      `json:"runtime"`
	ACPPort           int32       `json:"acpPort"`
	ContextManagement interface{} `json:"contextManagement,omitempty"`
}

// AgentRuntimeReconciler reconciles a AgentRuntime object
type AgentRuntimeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=forge.agentforge.dev,resources=agentruntimes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=forge.agentforge.dev,resources=agentruntimes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=forge.agentforge.dev,resources=agentruntimes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=keda.sh,resources=scaledjobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile watches AgentRuntime CRs and manages child resources:
// ConfigMap (agent registry), Service (A2A port), and either a KEDA ScaledJob
// or a Deployment depending on spec.scaling.mode.
func (r *AgentRuntimeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the AgentRuntime CR.
	var rt forgev1alpha1.AgentRuntime
	if err := r.Get(ctx, req.NamespacedName, &rt); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("AgentRuntime not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching AgentRuntime: %w", err)
	}

	// Set Progressing condition at start of reconciliation.
	apimeta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
		Type:               conditionTypeProgressing,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciling",
		Message:            "Reconciliation in progress",
		ObservedGeneration: rt.Generation,
	})

	// Reconcile child resources.
	var reconcileErr error
	if err := r.reconcileConfigMap(ctx, &rt); err != nil {
		reconcileErr = fmt.Errorf("reconciling ConfigMap: %w", err)
	}
	if reconcileErr == nil {
		if err := r.reconcileService(ctx, &rt); err != nil {
			reconcileErr = fmt.Errorf("reconciling Service: %w", err)
		}
	}
	if reconcileErr == nil {
		if err := r.reconcileWorkload(ctx, &rt); err != nil {
			reconcileErr = fmt.Errorf("reconciling workload: %w", err)
		}
	}

	// Update status based on reconciliation result.
	rt.Status.ObservedGeneration = rt.Generation
	if reconcileErr != nil {
		apimeta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               conditionTypeDegraded,
			Status:             metav1.ConditionTrue,
			Reason:             "ReconcileError",
			Message:            reconcileErr.Error(),
			ObservedGeneration: rt.Generation,
		})
		apimeta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               conditionTypeAvailable,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconcileError",
			Message:            reconcileErr.Error(),
			ObservedGeneration: rt.Generation,
		})
		apimeta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               conditionTypeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconcileFailed",
			Message:            reconcileErr.Error(),
			ObservedGeneration: rt.Generation,
		})
	} else {
		apimeta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               conditionTypeAvailable,
			Status:             metav1.ConditionTrue,
			Reason:             "ReconcileSuccess",
			Message:            "All child resources reconciled successfully",
			ObservedGeneration: rt.Generation,
		})
		apimeta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               conditionTypeDegraded,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconcileSuccess",
			Message:            "All child resources reconciled successfully",
			ObservedGeneration: rt.Generation,
		})
		apimeta.SetStatusCondition(&rt.Status.Conditions, metav1.Condition{
			Type:               conditionTypeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconcileComplete",
			Message:            "Reconciliation complete",
			ObservedGeneration: rt.Generation,
		})
	}

	if err := r.Status().Update(ctx, &rt); err != nil {
		log.Error(err, "Failed to update AgentRuntime status")
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	if reconcileErr != nil {
		return ctrl.Result{}, reconcileErr
	}

	log.Info("AgentRuntime reconciled successfully")
	return ctrl.Result{}, nil
}

// reconcileConfigMap creates or updates the agent registry ConfigMap.
func (r *AgentRuntimeReconciler) reconcileConfigMap(ctx context.Context, rt *forgev1alpha1.AgentRuntime) error {
	cmName := rt.Name + configMapSuffix

	// Build agent registry entry JSON.
	registryJSON, err := r.buildRegistryJSON(rt)
	if err != nil {
		return fmt.Errorf("building registry JSON: %w", err)
	}

	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: rt.Namespace,
			Labels:    childLabels(rt),
		},
		Data: map[string]string{
			"agent-registry.json": registryJSON,
		},
	}

	if err := controllerutil.SetControllerReference(rt, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on ConfigMap: %w", err)
	}

	// Create or update.
	var existing corev1.ConfigMap
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("creating ConfigMap: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting ConfigMap: %w", err)
	} else {
		existing.Data = desired.Data
		existing.Labels = desired.Labels
		if err := r.Update(ctx, &existing); err != nil {
			return fmt.Errorf("updating ConfigMap: %w", err)
		}
	}

	rt.Status.ConfigMapName = cmName
	return nil
}

// reconcileService creates or updates the ClusterIP Service for the A2A port.
func (r *AgentRuntimeReconciler) reconcileService(ctx context.Context, rt *forgev1alpha1.AgentRuntime) error {
	svcName := rt.Name + serviceSuffix
	port := rt.Spec.Container.Port
	if port == 0 {
		port = 8080
	}

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: rt.Namespace,
			Labels:    childLabels(rt),
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app.kubernetes.io/name":       "agentruntime",
				"app.kubernetes.io/instance":   rt.Name,
				"app.kubernetes.io/managed-by": "agent-forge-operator",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "a2a",
					Protocol:   corev1.ProtocolTCP,
					Port:       port,
					TargetPort: intstr.FromInt32(port),
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(rt, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on Service: %w", err)
	}

	var existing corev1.Service
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("creating Service: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting Service: %w", err)
	} else {
		existing.Spec.Ports = desired.Spec.Ports
		existing.Spec.Selector = desired.Spec.Selector
		existing.Labels = desired.Labels
		if err := r.Update(ctx, &existing); err != nil {
			return fmt.Errorf("updating Service: %w", err)
		}
	}

	rt.Status.ServiceName = svcName
	return nil
}

// reconcileWorkload creates or updates the workload (ScaledJob or Deployment)
// based on the scaling mode.
func (r *AgentRuntimeReconciler) reconcileWorkload(ctx context.Context, rt *forgev1alpha1.AgentRuntime) error {
	mode := "ScaledJob" // default
	if rt.Spec.Scaling != nil && rt.Spec.Scaling.Mode != "" {
		mode = rt.Spec.Scaling.Mode
	}

	switch mode {
	case "ScaledJob":
		if err := r.deleteDeploymentIfExists(ctx, rt); err != nil {
			return err
		}
		return r.reconcileScaledJob(ctx, rt)
	case "Static":
		if err := r.deleteScaledJobIfExists(ctx, rt); err != nil {
			return err
		}
		return r.reconcileDeployment(ctx, rt)
	default:
		return fmt.Errorf("unsupported scaling mode: %s", mode)
	}
}

// deleteDeploymentIfExists removes an existing Deployment for this runtime if present.
func (r *AgentRuntimeReconciler) deleteDeploymentIfExists(ctx context.Context, rt *forgev1alpha1.AgentRuntime) error {
	dep := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKey{Namespace: rt.Namespace, Name: rt.Name}, dep)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, dep)
}

// deleteScaledJobIfExists removes an existing ScaledJob for this runtime if present.
func (r *AgentRuntimeReconciler) deleteScaledJobIfExists(ctx context.Context, rt *forgev1alpha1.AgentRuntime) error {
	sj := &unstructured.Unstructured{}
	sj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledJob",
	})
	err := r.Get(ctx, client.ObjectKey{Namespace: rt.Namespace, Name: rt.Name}, sj)
	if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, sj)
}

// reconcileScaledJob creates or updates a KEDA ScaledJob using unstructured.
func (r *AgentRuntimeReconciler) reconcileScaledJob(ctx context.Context, rt *forgev1alpha1.AgentRuntime) error {
	sjName := rt.Name

	// Build the ScaledJob manifest.
	sj := r.buildScaledJob(rt, sjName)

	// Set owner reference manually on unstructured.
	if err := controllerutil.SetControllerReference(rt, sj, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on ScaledJob: %w", err)
	}

	// Check if it exists.
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(scaledJobGVK)
	err := r.Get(ctx, client.ObjectKey{Name: sjName, Namespace: rt.Namespace}, existing)
	if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
		if err := r.Create(ctx, sj); err != nil {
			return fmt.Errorf("creating ScaledJob: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting ScaledJob: %w", err)
	} else {
		// Update spec fields on existing resource.
		sj.SetResourceVersion(existing.GetResourceVersion())
		if err := r.Update(ctx, sj); err != nil {
			return fmt.Errorf("updating ScaledJob: %w", err)
		}
	}

	rt.Status.ScaledJobName = sjName
	return nil
}

// reconcileDeployment creates or updates a Deployment for static scaling mode.
func (r *AgentRuntimeReconciler) reconcileDeployment(ctx context.Context, rt *forgev1alpha1.AgentRuntime) error {
	deployName := rt.Name
	replicas := int32(1)
	if rt.Spec.Scaling != nil && rt.Spec.Scaling.Replicas != nil {
		replicas = *rt.Spec.Scaling.Replicas
	}

	port := rt.Spec.Container.Port
	if port == 0 {
		port = 8080
	}

	podLabels := map[string]string{
		"app.kubernetes.io/name":       "agentruntime",
		"app.kubernetes.io/instance":   rt.Name,
		"app.kubernetes.io/managed-by": "agent-forge-operator",
	}

	containers, err := r.buildContainers(rt, port)
	if err != nil {
		return fmt.Errorf("building containers: %w", err)
	}

	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: rt.Namespace,
			Labels:    childLabels(rt),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
				},
				Spec: corev1.PodSpec{
					Containers:       containers,
					ImagePullSecrets: imagePullSecrets(rt),
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(rt, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on Deployment: %w", err)
	}

	var existing appsv1.Deployment
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("creating Deployment: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting Deployment: %w", err)
	} else {
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		if err := r.Update(ctx, &existing); err != nil {
			return fmt.Errorf("updating Deployment: %w", err)
		}
	}

	// Clear ScaledJob name since we use Deployment in static mode.
	rt.Status.ScaledJobName = ""
	return nil
}

// buildScaledJob constructs an unstructured KEDA ScaledJob.
func (r *AgentRuntimeReconciler) buildScaledJob(rt *forgev1alpha1.AgentRuntime, name string) *unstructured.Unstructured {
	port := rt.Spec.Container.Port
	if port == 0 {
		port = 8080
	}

	// KEDA spec defaults.
	pollingInterval := int64(15)
	maxReplicaCount := int64(5)
	activeDeadlineSeconds := int64(600)
	backoffLimit := int64(2)
	lagThreshold := "1"
	activationLagThreshold := "0"

	if rt.Spec.Scaling != nil && rt.Spec.Scaling.KEDA != nil {
		keda := rt.Spec.Scaling.KEDA
		if keda.PollingInterval > 0 {
			pollingInterval = int64(keda.PollingInterval)
		}
		if keda.MaxReplicaCount > 0 {
			maxReplicaCount = int64(keda.MaxReplicaCount)
		}
		if keda.ActiveDeadlineSeconds > 0 {
			activeDeadlineSeconds = keda.ActiveDeadlineSeconds
		}
		if keda.BackoffLimit > 0 {
			backoffLimit = int64(keda.BackoffLimit)
		}
		if keda.LagThreshold != "" {
			lagThreshold = keda.LagThreshold
		}
		if keda.ActivationLagThreshold != "" {
			activationLagThreshold = keda.ActivationLagThreshold
		}
	}

	// Build container env vars.
	envVars := buildEnvVars(rt)

	// Worker container.
	workerContainer := map[string]interface{}{
		"name":  "agent",
		"image": rt.Spec.Container.Image,
		"ports": []interface{}{
			map[string]interface{}{
				"containerPort": int64(port),
				"name":          "a2a",
				"protocol":      "TCP",
			},
		},
		"env": envVars,
	}
	if rt.Spec.Container.ImagePullPolicy != "" {
		workerContainer["imagePullPolicy"] = rt.Spec.Container.ImagePullPolicy
	}
	if resources := buildResourceRequirements(rt); resources != nil {
		workerContainer["resources"] = resources
	}

	// Proxy sidecar container.
	proxyEnv := []interface{}{
		map[string]interface{}{
			"name":  "AGENT_PORT",
			"value": fmt.Sprintf("%d", port),
		},
		map[string]interface{}{
			"name":  "NATS_SUBJECT",
			"value": rt.Spec.Queue.Subject,
		},
		map[string]interface{}{
			"name":  "NATS_STREAM",
			"value": queueStream(rt),
		},
	}
	proxyEnv = append(proxyEnv, contextManagementEnvVars(rt)...)

	proxyContainer := map[string]interface{}{
		"name":  "proxy",
		"image": proxyImage,
		"ports": []interface{}{
			map[string]interface{}{
				"containerPort": int64(9090),
				"name":          "proxy",
				"protocol":      "TCP",
			},
		},
		"env": proxyEnv,
	}

	// Build pod spec.
	podSpec := map[string]interface{}{
		"containers":    []interface{}{workerContainer, proxyContainer},
		"restartPolicy": "Never",
	}
	if secrets := imagePullSecretsUnstructured(rt); len(secrets) > 0 {
		podSpec["imagePullSecrets"] = secrets
	}

	// Queue metadata for NATS JetStream trigger.
	stream := queueStream(rt)
	durableName := rt.Spec.Queue.DurableName
	if durableName == "" {
		durableName = rt.Name
	}

	sj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "keda.sh/v1alpha1",
			"kind":       "ScaledJob",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": rt.Namespace,
				"labels":    childLabelsMap(rt),
			},
			"spec": map[string]interface{}{
				"pollingInterval": pollingInterval,
				"maxReplicaCount": maxReplicaCount,
				"jobTargetRef": map[string]interface{}{
					"activeDeadlineSeconds": activeDeadlineSeconds,
					"backoffLimit":          backoffLimit,
					"template": map[string]interface{}{
						"metadata": map[string]interface{}{
							"labels": map[string]interface{}{
								"app.kubernetes.io/name":       "agentruntime",
								"app.kubernetes.io/instance":   rt.Name,
								"app.kubernetes.io/managed-by": "agent-forge-operator",
							},
						},
						"spec": podSpec,
					},
				},
				"triggers": []interface{}{
					map[string]interface{}{
						"type": "nats-jetstream",
						"metadata": map[string]interface{}{
							"natsServerMonitoringEndpoint": natsMonitoringEndpoint(rt),
							"account":                      "$G",
							"stream":                       stream,
							"consumer":                     durableName,
							"lagThreshold":                 lagThreshold,
							"activationLagThreshold":       activationLagThreshold,
						},
					},
				},
			},
		},
	}

	return sj
}

// buildContainers builds the container list for Deployment pods.
func (r *AgentRuntimeReconciler) buildContainers(rt *forgev1alpha1.AgentRuntime, port int32) ([]corev1.Container, error) {
	envVars := buildEnvVarsTyped(rt)

	agentContainer := corev1.Container{
		Name:  "agent",
		Image: rt.Spec.Container.Image,
		Ports: []corev1.ContainerPort{
			{
				Name:          "a2a",
				ContainerPort: port,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env: envVars,
	}
	if rt.Spec.Container.ImagePullPolicy != "" {
		agentContainer.ImagePullPolicy = corev1.PullPolicy(rt.Spec.Container.ImagePullPolicy)
	}
	if rt.Spec.Container.Resources != nil {
		rr, err := buildResourceRequirementsTyped(rt)
		if err != nil {
			return nil, fmt.Errorf("building resource requirements: %w", err)
		}
		agentContainer.Resources = rr
	}

	proxyContainer := corev1.Container{
		Name:  "proxy",
		Image: proxyImage,
		Ports: []corev1.ContainerPort{
			{
				Name:          "proxy",
				ContainerPort: 9090,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env: append([]corev1.EnvVar{
			{Name: "AGENT_PORT", Value: fmt.Sprintf("%d", port)},
			{Name: "NATS_SUBJECT", Value: rt.Spec.Queue.Subject},
			{Name: "NATS_STREAM", Value: queueStream(rt)},
		}, contextManagementEnvVarsTyped(rt)...),
	}

	return []corev1.Container{agentContainer, proxyContainer}, nil
}

// buildRegistryJSON creates the agent registry entry JSON.
func (r *AgentRuntimeReconciler) buildRegistryJSON(rt *forgev1alpha1.AgentRuntime) (string, error) {
	port := rt.Spec.Container.Port
	if port == 0 {
		port = 8080
	}

	var card interface{}
	if rt.Spec.AgentCard != nil {
		card = rt.Spec.AgentCard
	} else if rt.Spec.AgentCardRef != nil {
		card = map[string]string{
			"ref":  rt.Spec.AgentCardRef.Name,
			"kind": rt.Spec.AgentCardRef.Kind,
		}
	} else {
		// Minimal card from the runtime name.
		card = map[string]string{
			"name": rt.Name,
		}
	}

	entry := agentRegistryEntry{
		Card:         card,
		QueueSubject: rt.Spec.Queue.Subject,
		Runtime:      string(rt.Spec.Type),
		ACPPort:      port,
	}
	if rt.Spec.ContextManagement != nil {
		entry.ContextManagement = rt.Spec.ContextManagement
	}

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshalling registry entry: %w", err)
	}
	return string(data), nil
}

// contextManagementEnvVars returns context management env vars as []interface{}
// for the unstructured ScaledJob path. Returns nil if context management is not configured.
func contextManagementEnvVars(rt *forgev1alpha1.AgentRuntime) []interface{} {
	if rt.Spec.ContextManagement == nil {
		return nil
	}
	cm := rt.Spec.ContextManagement
	envs := []interface{}{
		map[string]interface{}{
			"name":  "CONTEXT_COMPACTION_INTERVAL",
			"value": cm.CompactionInterval,
		},
		map[string]interface{}{
			"name":  "CONTEXT_TOKEN_THRESHOLD",
			"value": fmt.Sprintf("%d", cm.TokenThreshold),
		},
		map[string]interface{}{
			"name":  "CONTEXT_EVENT_RETENTION_SIZE",
			"value": fmt.Sprintf("%d", cm.EventRetentionSize),
		},
		map[string]interface{}{
			"name":  "CONTEXT_OVERLAP_SIZE",
			"value": fmt.Sprintf("%d", cm.OverlapSize),
		},
	}
	if cm.Resurrection != nil {
		envs = append(envs,
			map[string]interface{}{
				"name":  "CONTEXT_RESURRECTION_FULL_THRESHOLD",
				"value": fmt.Sprintf("%d", cm.Resurrection.FullThreshold),
			},
			map[string]interface{}{
				"name":  "CONTEXT_RESURRECTION_CHECKPOINT_THRESHOLD",
				"value": fmt.Sprintf("%d", cm.Resurrection.CheckpointThreshold),
			},
		)
	}
	return envs
}

// contextManagementEnvVarsTyped returns context management env vars as []corev1.EnvVar
// for the typed Deployment path. Returns nil if context management is not configured.
func contextManagementEnvVarsTyped(rt *forgev1alpha1.AgentRuntime) []corev1.EnvVar {
	if rt.Spec.ContextManagement == nil {
		return nil
	}
	cm := rt.Spec.ContextManagement
	envs := []corev1.EnvVar{
		{Name: "CONTEXT_COMPACTION_INTERVAL", Value: cm.CompactionInterval},
		{Name: "CONTEXT_TOKEN_THRESHOLD", Value: fmt.Sprintf("%d", cm.TokenThreshold)},
		{Name: "CONTEXT_EVENT_RETENTION_SIZE", Value: fmt.Sprintf("%d", cm.EventRetentionSize)},
		{Name: "CONTEXT_OVERLAP_SIZE", Value: fmt.Sprintf("%d", cm.OverlapSize)},
	}
	if cm.Resurrection != nil {
		envs = append(envs,
			corev1.EnvVar{Name: "CONTEXT_RESURRECTION_FULL_THRESHOLD", Value: fmt.Sprintf("%d", cm.Resurrection.FullThreshold)},
			corev1.EnvVar{Name: "CONTEXT_RESURRECTION_CHECKPOINT_THRESHOLD", Value: fmt.Sprintf("%d", cm.Resurrection.CheckpointThreshold)},
		)
	}
	return envs
}

// --- Helper functions ---

// childLabels returns standard labels for child resources.
func childLabels(rt *forgev1alpha1.AgentRuntime) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "agentruntime",
		"app.kubernetes.io/instance":   rt.Name,
		"app.kubernetes.io/managed-by": "agent-forge-operator",
	}
}

// childLabelsMap returns labels as map[string]interface{} for unstructured objects.
func childLabelsMap(rt *forgev1alpha1.AgentRuntime) map[string]interface{} {
	return map[string]interface{}{
		"app.kubernetes.io/name":       "agentruntime",
		"app.kubernetes.io/instance":   rt.Name,
		"app.kubernetes.io/managed-by": "agent-forge-operator",
	}
}

// queueStream returns the NATS stream name, defaulting to "AGENT_TASKS".
func queueStream(rt *forgev1alpha1.AgentRuntime) string {
	if rt.Spec.Queue.Stream != "" {
		return rt.Spec.Queue.Stream
	}
	return "AGENT_TASKS"
}

// natsMonitoringEndpoint returns the NATS HTTP monitoring endpoint from KEDASpec,
// falling back to the default "nats.nats.svc.cluster.local:8222".
func natsMonitoringEndpoint(rt *forgev1alpha1.AgentRuntime) string {
	if rt.Spec.Scaling != nil && rt.Spec.Scaling.KEDA != nil && rt.Spec.Scaling.KEDA.NATSMonitoringEndpoint != "" {
		return rt.Spec.Scaling.KEDA.NATSMonitoringEndpoint
	}
	return "nats.nats.svc.cluster.local:8222"
}

// buildEnvVars builds env vars as []interface{} for unstructured (ScaledJob).
func buildEnvVars(rt *forgev1alpha1.AgentRuntime) []interface{} {
	var envs []interface{}
	for _, v := range rt.Spec.Env {
		if v.ValueFrom != nil {
			if v.ValueFrom.SecretKeyRef != nil {
				envs = append(envs, map[string]interface{}{
					"name": v.Name,
					"valueFrom": map[string]interface{}{
						"secretKeyRef": map[string]interface{}{
							"name": v.ValueFrom.SecretKeyRef.Name,
							"key":  v.ValueFrom.SecretKeyRef.Key,
						},
					},
				})
			} else if v.ValueFrom.ConfigMapKeyRef != nil {
				envs = append(envs, map[string]interface{}{
					"name": v.Name,
					"valueFrom": map[string]interface{}{
						"configMapKeyRef": map[string]interface{}{
							"name": v.ValueFrom.ConfigMapKeyRef.Name,
							"key":  v.ValueFrom.ConfigMapKeyRef.Key,
						},
					},
				})
			}
		} else {
			envs = append(envs, map[string]interface{}{
				"name":  v.Name,
				"value": v.Value,
			})
		}
	}
	return envs
}

// buildEnvVarsTyped builds env vars as []corev1.EnvVar for typed Deployment.
func buildEnvVarsTyped(rt *forgev1alpha1.AgentRuntime) []corev1.EnvVar {
	var envs []corev1.EnvVar
	for _, v := range rt.Spec.Env {
		if v.ValueFrom != nil {
			if v.ValueFrom.SecretKeyRef != nil {
				envs = append(envs, corev1.EnvVar{
					Name: v.Name,
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: v.ValueFrom.SecretKeyRef.Name},
							Key:                  v.ValueFrom.SecretKeyRef.Key,
						},
					},
				})
			} else if v.ValueFrom.ConfigMapKeyRef != nil {
				envs = append(envs, corev1.EnvVar{
					Name: v.Name,
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: v.ValueFrom.ConfigMapKeyRef.Name},
							Key:                  v.ValueFrom.ConfigMapKeyRef.Key,
						},
					},
				})
			}
			// If neither ref is set, drop the env var (same as ScaledJob path)
		} else {
			envs = append(envs, corev1.EnvVar{
				Name:  v.Name,
				Value: v.Value,
			})
		}
	}
	return envs
}

// buildResourceRequirements returns resource requirements as map for unstructured.
func buildResourceRequirements(rt *forgev1alpha1.AgentRuntime) map[string]interface{} {
	if rt.Spec.Container.Resources == nil {
		return nil
	}
	res := map[string]interface{}{}
	if req := rt.Spec.Container.Resources.Requests; req != nil {
		reqs := map[string]interface{}{}
		if req.CPU != "" {
			reqs["cpu"] = req.CPU
		}
		if req.Memory != "" {
			reqs["memory"] = req.Memory
		}
		if len(reqs) > 0 {
			res["requests"] = reqs
		}
	}
	if lim := rt.Spec.Container.Resources.Limits; lim != nil {
		lims := map[string]interface{}{}
		if lim.CPU != "" {
			lims["cpu"] = lim.CPU
		}
		if lim.Memory != "" {
			lims["memory"] = lim.Memory
		}
		if len(lims) > 0 {
			res["limits"] = lims
		}
	}
	if len(res) == 0 {
		return nil
	}
	return res
}

// buildResourceRequirementsTyped returns typed resource requirements for Deployment.
func buildResourceRequirementsTyped(rt *forgev1alpha1.AgentRuntime) (corev1.ResourceRequirements, error) {
	rr := corev1.ResourceRequirements{}
	if rt.Spec.Container.Resources == nil {
		return rr, nil
	}
	if req := rt.Spec.Container.Resources.Requests; req != nil {
		rr.Requests = corev1.ResourceList{}
		if req.CPU != "" {
			q, err := parseQuantity(req.CPU)
			if err != nil {
				return rr, fmt.Errorf("parsing requests.cpu %q: %w", req.CPU, err)
			}
			rr.Requests[corev1.ResourceCPU] = q
		}
		if req.Memory != "" {
			q, err := parseQuantity(req.Memory)
			if err != nil {
				return rr, fmt.Errorf("parsing requests.memory %q: %w", req.Memory, err)
			}
			rr.Requests[corev1.ResourceMemory] = q
		}
	}
	if lim := rt.Spec.Container.Resources.Limits; lim != nil {
		rr.Limits = corev1.ResourceList{}
		if lim.CPU != "" {
			q, err := parseQuantity(lim.CPU)
			if err != nil {
				return rr, fmt.Errorf("parsing limits.cpu %q: %w", lim.CPU, err)
			}
			rr.Limits[corev1.ResourceCPU] = q
		}
		if lim.Memory != "" {
			q, err := parseQuantity(lim.Memory)
			if err != nil {
				return rr, fmt.Errorf("parsing limits.memory %q: %w", lim.Memory, err)
			}
			rr.Limits[corev1.ResourceMemory] = q
		}
	}
	return rr, nil
}

// parseQuantity parses a resource quantity string, returning an error on failure.
func parseQuantity(s string) (resource.Quantity, error) {
	if s == "" {
		return resource.Quantity{}, nil
	}
	return resource.ParseQuantity(s)
}

// imagePullSecrets returns typed ImagePullSecrets for Deployment pods.
func imagePullSecrets(rt *forgev1alpha1.AgentRuntime) []corev1.LocalObjectReference {
	var refs []corev1.LocalObjectReference
	for _, name := range rt.Spec.Container.ImagePullSecrets {
		refs = append(refs, corev1.LocalObjectReference{Name: name})
	}
	return refs
}

// imagePullSecretsUnstructured returns image pull secrets for unstructured pods.
func imagePullSecretsUnstructured(rt *forgev1alpha1.AgentRuntime) []interface{} {
	var refs []interface{}
	for _, name := range rt.Spec.Container.ImagePullSecrets {
		refs = append(refs, map[string]interface{}{
			"name": name,
		})
	}
	return refs
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRuntimeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&forgev1alpha1.AgentRuntime{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.Deployment{})

	// Watch KEDA ScaledJobs if CRD is installed
	scaledJob := &unstructured.Unstructured{}
	scaledJob.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "keda.sh",
		Version: "v1alpha1",
		Kind:    "ScaledJob",
	})

	// Check if KEDA CRD exists by attempting discovery
	_, err := mgr.GetRESTMapper().RESTMapping(
		schema.GroupKind{Group: "keda.sh", Kind: "ScaledJob"},
		"v1alpha1",
	)
	if err == nil {
		builder = builder.Owns(scaledJob)
	} else {
		log := mgr.GetLogger()
		log.Info("KEDA ScaledJob CRD not found, skipping watch — ScaledJob changes won't trigger reconciliation")
	}

	return builder.Named("agentruntime").Complete(r)
}
