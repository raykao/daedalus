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

// AgentCardSpec defines the desired state of AgentCard.
// An AgentCard is a standalone Kubernetes resource representing A2A capability discovery.
// It can be referenced by multiple AgentRuntimes or used directly by the orchestrator
// for routing and discovery.
type AgentCardSpec struct {
	// name is the agent's display name.
	// +required
	AgentName string `json:"agentName"`

	// description is a human-readable description of the agent.
	// +required
	Description string `json:"description"`

	// version is the agent version string.
	// +kubebuilder:default="1.0.0"
	// +optional
	Version string `json:"version,omitempty"`

	// skills lists the agent's capabilities for routing and discovery.
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

	// url is an optional A2A HTTP endpoint URL for external agents.
	// +optional
	URL string `json:"url,omitempty"`
}

// AgentCardStatus defines the observed state of AgentCard.
type AgentCardStatus struct {
	// conditions represent the current state of the AgentCard resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// registeredRuntimes lists the AgentRuntime names that reference this card.
	// +optional
	RegisteredRuntimes []string `json:"registeredRuntimes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// AgentCard is the Schema for the agentcards API
type AgentCard struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of AgentCard
	// +required
	Spec AgentCardSpec `json:"spec"`

	// status defines the observed state of AgentCard
	// +optional
	Status AgentCardStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AgentCardList contains a list of AgentCard
type AgentCardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AgentCard `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentCard{}, &AgentCardList{})
}
