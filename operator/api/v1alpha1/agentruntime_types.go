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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentRuntimeSpec defines the desired state of AgentRuntime.
// An AgentRuntime represents a worker type: container image, scaling config,
// queue binding, and AgentCard for capability discovery.
type AgentRuntimeSpec struct {
	// type distinguishes platform-managed (Declarative) from user-provided (BYO) runtimes.
	// Declarative runtimes use copilot-bridge + .agent.md files.
	// BYO runtimes are user containers speaking A2A.
	// +kubebuilder:default=BYO
	// +required
	Type AgentRuntimeType `json:"type"`

	// conformanceLevel declares the A2A contract conformance level.
	// Level1=minimal (health+AgentCard+task), Level2=production (+SIGTERM+logging+OTel),
	// Level3=full (+streaming+context compaction+session resume).
	// +kubebuilder:default=Level1
	// +required
	ConformanceLevel ConformanceLevel `json:"conformanceLevel"`

	// container defines the agent runtime container configuration.
	// +required
	Container ContainerSpec `json:"container"`

	// queue binds this runtime to a NATS subject for task dispatch.
	// +required
	Queue QueueBinding `json:"queue"`

	// scaling configures how this runtime scales (KEDA ScaledJob or static replicas).
	// +optional
	Scaling *ScalingSpec `json:"scaling,omitempty"`

	// agentCard is an inline AgentCard for capability discovery.
	// Mutually exclusive with agentCardRef.
	// +optional
	AgentCard *AgentCardInline `json:"agentCard,omitempty"`

	// agentCardRef references a standalone AgentCard CR.
	// Mutually exclusive with agentCard.
	// +optional
	AgentCardRef *TypedReference `json:"agentCardRef,omitempty"`

	// modelConfigRef references a ModelConfig CR for LLM provider configuration.
	// +optional
	ModelConfigRef *TypedReference `json:"modelConfigRef,omitempty"`

	// mcpServers lists MCP tool servers available to this runtime.
	// +optional
	MCPServers []TypedReference `json:"mcpServers,omitempty"`

	// contextManagement configures context window compression (Level 3 only).
	// +optional
	ContextManagement *ContextManagementSpec `json:"contextManagement,omitempty"`

	// allowedNamespaces defines which namespaces may reference this AgentRuntime.
	// +optional
	AllowedNamespaces *AllowedNamespaces `json:"allowedNamespaces,omitempty"`

	// env specifies additional environment variables for the runtime container.
	// +optional
	Env []ValueRef `json:"env,omitempty"`
}

// ContainerSpec defines the agent runtime container image and resources.
type ContainerSpec struct {
	// image is the container image reference (e.g., "ghcr.io/raykao/my-agent:v1").
	// +required
	Image string `json:"image"`

	// port is the A2A server port inside the container.
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`

	// workDir is the writable working directory mounted into the container.
	// +kubebuilder:default="/workspace"
	// +optional
	WorkDir string `json:"workDir,omitempty"`

	// resources defines CPU/memory requests and limits.
	// +optional
	Resources *ResourceSpec `json:"resources,omitempty"`

	// imagePullPolicy specifies when to pull the container image.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +kubebuilder:default=IfNotPresent
	// +optional
	ImagePullPolicy string `json:"imagePullPolicy,omitempty"`

	// imagePullSecrets lists secrets for pulling from private registries.
	// +optional
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`

	// startupTimeout is how long to wait for the agent's /health endpoint.
	// +kubebuilder:default="60s"
	// +optional
	StartupTimeout string `json:"startupTimeout,omitempty"`

	// terminationGracePeriod is the SIGTERM grace period.
	// +kubebuilder:default="30s"
	// +optional
	TerminationGracePeriod string `json:"terminationGracePeriod,omitempty"`
}

// ResourceSpec mirrors Kubernetes resource requirements.
type ResourceSpec struct {
	// requests defines minimum resources.
	// +optional
	Requests *ResourceQuantity `json:"requests,omitempty"`

	// limits defines maximum resources.
	// +optional
	Limits *ResourceQuantity `json:"limits,omitempty"`
}

// ResourceQuantity defines CPU and memory amounts.
type ResourceQuantity struct {
	// cpu is the CPU amount (e.g., "100m", "0.5", "2").
	// +optional
	CPU string `json:"cpu,omitempty"`

	// memory is the memory amount (e.g., "128Mi", "1Gi").
	// +optional
	Memory string `json:"memory,omitempty"`
}

// QueueBinding binds the runtime to a NATS subject for task dispatch.
type QueueBinding struct {
	// subject is the NATS subject to consume from (e.g., "agent.tasks.copilot").
	// +required
	Subject string `json:"subject"`

	// stream is the NATS JetStream stream name.
	// +kubebuilder:default="AGENT_TASKS"
	// +optional
	Stream string `json:"stream,omitempty"`

	// durableName is the NATS durable consumer name. Defaults to the AgentRuntime name.
	// +optional
	DurableName string `json:"durableName,omitempty"`
}

// ScalingSpec configures how the runtime scales.
type ScalingSpec struct {
	// mode selects the scaling strategy.
	// +kubebuilder:validation:Enum=Static;ScaledJob
	// +kubebuilder:default=ScaledJob
	// +required
	Mode string `json:"mode"`

	// replicas is the static replica count (only used when mode=Static).
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// keda configures KEDA ScaledJob parameters (only used when mode=ScaledJob).
	// +optional
	KEDA *KEDASpec `json:"keda,omitempty"`
}

// KEDASpec defines KEDA ScaledJob configuration.
type KEDASpec struct {
	// pollingInterval is how often KEDA checks the trigger source (seconds).
	// +kubebuilder:default=15
	// +optional
	PollingInterval int32 `json:"pollingInterval,omitempty"`

	// maxReplicaCount is the maximum number of job pods.
	// +kubebuilder:default=5
	// +optional
	MaxReplicaCount int32 `json:"maxReplicaCount,omitempty"`

	// activeDeadlineSeconds is the job timeout.
	// +kubebuilder:default=600
	// +optional
	ActiveDeadlineSeconds int64 `json:"activeDeadlineSeconds,omitempty"`

	// backoffLimit is the job retry count.
	// +kubebuilder:default=2
	// +optional
	BackoffLimit int32 `json:"backoffLimit,omitempty"`

	// lagThreshold triggers scale-up when queue lag exceeds this value.
	// +kubebuilder:default="1"
	// +optional
	LagThreshold string `json:"lagThreshold,omitempty"`

	// activationLagThreshold triggers scale-from-zero when lag exceeds this value.
	// +kubebuilder:default="0"
	// +optional
	ActivationLagThreshold string `json:"activationLagThreshold,omitempty"`
}

// ContextManagementSpec configures context window compression (Level 3 conformance).
type ContextManagementSpec struct {
	// compactionInterval is how often to check for context compaction needs.
	// +kubebuilder:default="5m"
	// +optional
	CompactionInterval string `json:"compactionInterval,omitempty"`

	// tokenThreshold triggers compaction when token count exceeds this value.
	// +kubebuilder:default=100000
	// +optional
	TokenThreshold int64 `json:"tokenThreshold,omitempty"`

	// eventRetentionSize is the number of recent events to keep after compaction.
	// +kubebuilder:default=50
	// +optional
	EventRetentionSize int32 `json:"eventRetentionSize,omitempty"`

	// summarizer configures the model used for context summarization.
	// +optional
	Summarizer *TypedReference `json:"summarizer,omitempty"`
}

// AgentCardInline embeds AgentCard fields directly in the AgentRuntime spec.
type AgentCardInline struct {
	// name is the agent's display name.
	// +required
	Name string `json:"name"`

	// description is a human-readable description of the agent.
	// +required
	Description string `json:"description"`

	// version is the agent version.
	// +kubebuilder:default="1.0.0"
	// +optional
	Version string `json:"version,omitempty"`

	// skills lists the agent's capabilities.
	// +optional
	Skills []SkillSpec `json:"skills,omitempty"`

	// defaultInputModes lists supported input content types.
	// +kubebuilder:default={"text/plain"}
	// +optional
	DefaultInputModes []string `json:"defaultInputModes,omitempty"`

	// defaultOutputModes lists supported output content types.
	// +kubebuilder:default={"text/plain"}
	// +optional
	DefaultOutputModes []string `json:"defaultOutputModes,omitempty"`

	// capabilities describes protocol-level capabilities.
	// +optional
	Capabilities *CapabilitiesSpec `json:"capabilities,omitempty"`
}

// SkillSpec describes a single agent capability.
type SkillSpec struct {
	// id is the unique skill identifier.
	// +required
	ID string `json:"id"`

	// name is the human-readable skill name.
	// +required
	Name string `json:"name"`

	// description describes what the skill does.
	// +required
	Description string `json:"description"`

	// tags categorize the skill for routing.
	// +optional
	Tags []string `json:"tags,omitempty"`

	// examples are example prompts that demonstrate the skill.
	// +optional
	Examples []string `json:"examples,omitempty"`
}

// CapabilitiesSpec describes protocol-level capabilities.
type CapabilitiesSpec struct {
	// streaming indicates SSE streaming support.
	// +kubebuilder:default=false
	// +optional
	Streaming bool `json:"streaming"`

	// pushNotifications indicates push notification support.
	// +kubebuilder:default=false
	// +optional
	PushNotifications bool `json:"pushNotifications"`
}

// AgentRuntimeStatus defines the observed state of AgentRuntime.
type AgentRuntimeStatus struct {
	// conditions represent the current state of the AgentRuntime resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// readyReplicas is the number of pods that are ready and serving.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// scaledJobName is the name of the KEDA ScaledJob created for this runtime.
	// +optional
	ScaledJobName string `json:"scaledJobName,omitempty"`

	// configMapName is the name of the ConfigMap created for this runtime.
	// +optional
	ConfigMapName string `json:"configMapName,omitempty"`

	// serviceName is the name of the Service created for this runtime.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// modelConfigHash tracks the hash of the referenced ModelConfig's secrets
	// to detect credential rotation.
	// +optional
	ModelConfigHash string `json:"modelConfigHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// AgentRuntime is the Schema for the agentruntimes API
type AgentRuntime struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AgentRuntime
	// +required
	Spec AgentRuntimeSpec `json:"spec"`

	// status defines the observed state of AgentRuntime
	// +optional
	Status AgentRuntimeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AgentRuntimeList contains a list of AgentRuntime
type AgentRuntimeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AgentRuntime `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRuntime{}, &AgentRuntimeList{})
}
