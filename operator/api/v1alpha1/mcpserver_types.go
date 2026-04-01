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

// MCPProtocol specifies the MCP transport protocol.
// +kubebuilder:validation:Enum=SSE;StreamableHTTP;Stdio
type MCPProtocol string

const (
	// MCPProtocolSSE uses Server-Sent Events transport.
	MCPProtocolSSE MCPProtocol = "SSE"

	// MCPProtocolStreamableHTTP uses Streamable HTTP transport.
	MCPProtocolStreamableHTTP MCPProtocol = "StreamableHTTP"

	// MCPProtocolStdio uses standard I/O transport (sidecar process).
	MCPProtocolStdio MCPProtocol = "Stdio"
)

// MCPServerSpec defines the desired state of MCPServer.
// An MCPServer represents a shared MCP tool server that agent runtimes can consume.
type MCPServerSpec struct {
	// protocol specifies the MCP transport (SSE, StreamableHTTP, or Stdio).
	// +kubebuilder:default=StreamableHTTP
	// +required
	Protocol MCPProtocol `json:"protocol"`

	// url is the MCP server endpoint URL (for SSE or StreamableHTTP protocols).
	// +optional
	URL string `json:"url,omitempty"`

	// command is the command to run for Stdio protocol MCP servers.
	// +optional
	Command []string `json:"command,omitempty"`

	// headers are HTTP headers sent with MCP requests (e.g., authentication).
	// Uses ValueRef for flexible value resolution (inline, ConfigMap, Secret).
	// +optional
	Headers []ValueRef `json:"headers,omitempty"`

	// tools lists the tool names this MCP server provides.
	// Used for routing and discovery.
	// +optional
	Tools []string `json:"tools,omitempty"`

	// description is a human-readable description of this MCP server.
	// +optional
	Description string `json:"description,omitempty"`

	// allowedNamespaces defines which namespaces may reference this MCPServer.
	// +optional
	AllowedNamespaces *AllowedNamespaces `json:"allowedNamespaces,omitempty"`
}

// MCPServerStatus defines the observed state of MCPServer.
type MCPServerStatus struct {
	// conditions represent the current state of the MCPServer resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// availableTools lists the tools discovered from the MCP server.
	// +optional
	AvailableTools []string `json:"availableTools,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// MCPServer is the Schema for the mcpservers API
type MCPServer struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of MCPServer
	// +required
	Spec MCPServerSpec `json:"spec"`

	// status defines the observed state of MCPServer
	// +optional
	Status MCPServerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// MCPServerList contains a list of MCPServer
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []MCPServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPServer{}, &MCPServerList{})
}
