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

// ModelConfigSpec defines the desired state of ModelConfig.
// A ModelConfig represents LLM provider configuration: API keys, model parameters,
// and endpoint settings. Secret hash tracking in status enables credential rotation detection.
type ModelConfigSpec struct {
	// provider identifies the LLM provider (e.g., "github-copilot", "openai", "anthropic", "bedrock").
	// +required
	Provider string `json:"provider"`

	// model is the model identifier (e.g., "gpt-4o", "claude-sonnet-4-20250514", "copilot").
	// +required
	Model string `json:"model"`

	// apiEndpoint is the provider API base URL. Omit for default provider endpoints.
	// +optional
	APIEndpoint string `json:"apiEndpoint,omitempty"`

	// apiKey provides the API key via inline value or secret reference.
	// +optional
	APIKey *ValueRef `json:"apiKey,omitempty"`

	// parameters are model-specific parameters (temperature, max_tokens, etc.).
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// headers are additional HTTP headers sent with API requests.
	// +optional
	Headers []ValueRef `json:"headers,omitempty"`

	// allowedNamespaces defines which namespaces may reference this ModelConfig.
	// +optional
	AllowedNamespaces *AllowedNamespaces `json:"allowedNamespaces,omitempty"`
}

// ModelConfigStatus defines the observed state of ModelConfig.
type ModelConfigStatus struct {
	// conditions represent the current state of the ModelConfig resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// secretHash is a SHA-256 hash of the referenced secret data.
	// The controller reconciles downstream AgentRuntimes when this hash changes,
	// enabling automatic credential rotation propagation.
	// +optional
	SecretHash string `json:"secretHash,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ModelConfig is the Schema for the modelconfigs API
type ModelConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ModelConfig
	// +required
	Spec ModelConfigSpec `json:"spec"`

	// status defines the observed state of ModelConfig
	// +optional
	Status ModelConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ModelConfigList contains a list of ModelConfig
type ModelConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ModelConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ModelConfig{}, &ModelConfigList{})
}
