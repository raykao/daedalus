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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TypedReference is a cross-resource reference with kind, apiGroup, name,
// and optional namespace. Adopted from kagent's pattern for type-safe,
// namespace-aware references between CRDs.
type TypedReference struct {
	// kind is the Kubernetes resource kind (e.g., "ModelConfig", "MCPServer").
	// +required
	Kind string `json:"kind"`

	// apiGroup is the API group of the referenced resource.
	// +required
	APIGroup string `json:"apiGroup"`

	// name is the name of the referenced resource.
	// +required
	Name string `json:"name"`

	// namespace is the namespace of the referenced resource.
	// If empty, defaults to the namespace of the referencing resource.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ValueRef provides a flexible way to specify configuration values.
// Values can be inline, from a ConfigMap, or from a Secret.
type ValueRef struct {
	// name is the key name for this value (e.g., "api-key", "model-name").
	// +required
	Name string `json:"name"`

	// value is an inline string value. Mutually exclusive with valueFrom.
	// +optional
	Value string `json:"value,omitempty"`

	// valueFrom specifies a source for the value. Mutually exclusive with value.
	// +optional
	ValueFrom *ValueSource `json:"valueFrom,omitempty"`
}

// SingleValueRef provides a flexible way to specify a single configuration value.
// Unlike ValueRef, it has no Name field - use this for singular fields like apiKey.
type SingleValueRef struct {
	// value is an inline string value. Mutually exclusive with valueFrom.
	// +optional
	Value string `json:"value,omitempty"`

	// valueFrom specifies a source for the value. Mutually exclusive with value.
	// +optional
	ValueFrom *ValueSource `json:"valueFrom,omitempty"`
}

// ValueSource describes where to find a value.
type ValueSource struct {
	// configMapKeyRef selects a key from a ConfigMap.
	// +optional
	ConfigMapKeyRef *KeySelector `json:"configMapKeyRef,omitempty"`

	// secretKeyRef selects a key from a Secret.
	// +optional
	SecretKeyRef *KeySelector `json:"secretKeyRef,omitempty"`
}

// KeySelector selects a specific key from a ConfigMap or Secret.
type KeySelector struct {
	// name is the name of the ConfigMap or Secret.
	// +required
	Name string `json:"name"`

	// key is the key within the ConfigMap or Secret.
	// +required
	Key string `json:"key"`
}

// FromNamespaces specifies which namespaces are allowed to reference a resource.
// +kubebuilder:validation:Enum=All;Same;Selector
type FromNamespaces string

const (
	// FromNamespacesAll allows references from all namespaces.
	FromNamespacesAll FromNamespaces = "All"

	// FromNamespacesSame allows references only from the same namespace.
	FromNamespacesSame FromNamespaces = "Same"

	// FromNamespacesSelector allows references from namespaces matching a label selector.
	FromNamespacesSelector FromNamespaces = "Selector"
)

// AllowedNamespaces defines which namespaces may reference this resource.
// Follows the Gateway API pattern for cross-namespace authorization.
type AllowedNamespaces struct {
	// from specifies the namespace scope.
	// +kubebuilder:default=Same
	// +required
	From FromNamespaces `json:"from"`

	// selector is a label selector for namespaces. Only used when from=Selector.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// ConformanceLevel indicates the A2A runtime contract conformance level.
// +kubebuilder:validation:Enum=Level1;Level2;Level3
type ConformanceLevel string

const (
	// ConformanceLevelMinimal requires health endpoint + AgentCard + task execution.
	ConformanceLevelMinimal ConformanceLevel = "Level1"

	// ConformanceLevelProduction adds SIGTERM graceful shutdown, JSON logging, OTel tracing.
	ConformanceLevelProduction ConformanceLevel = "Level2"

	// ConformanceLevelFull adds SSE streaming, context compaction, session resume.
	ConformanceLevelFull ConformanceLevel = "Level3"
)

// AgentRuntimeType distinguishes platform-managed from user-provided runtimes.
// +kubebuilder:validation:Enum=Declarative;BYO
type AgentRuntimeType string

const (
	// AgentRuntimeTypeDeclarative is a platform-managed runtime (e.g., copilot-bridge + .agent.md).
	AgentRuntimeTypeDeclarative AgentRuntimeType = "Declarative"

	// AgentRuntimeTypeBYO is a user-provided container image that speaks A2A.
	AgentRuntimeTypeBYO AgentRuntimeType = "BYO"
)
