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

// TaskPipelineSpec defines the desired state of TaskPipeline.
// A TaskPipeline declares multi-step workflow routing: fan-out/fan-in,
// dependency ordering, and result aggregation. Novel to Agent Forge (no kagent equivalent).
type TaskPipelineSpec struct {
	// description is a human-readable description of this pipeline.
	// +optional
	Description string `json:"description,omitempty"`

	// stages defines the ordered list of pipeline stages.
	// Stages execute sequentially; tasks within a stage can fan out in parallel.
	// +required
	// +kubebuilder:validation:MinItems=1
	Stages []PipelineStage `json:"stages"`

	// resultStrategy defines how to aggregate results from all stages.
	// +kubebuilder:validation:Enum=Last;Merge;Custom
	// +kubebuilder:default=Last
	// +optional
	ResultStrategy string `json:"resultStrategy,omitempty"`

	// timeout is the maximum duration for the entire pipeline.
	// +kubebuilder:default="30m"
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// PipelineStage defines a group of tasks that execute together within a pipeline.
type PipelineStage struct {
	// name identifies this stage (unique within the pipeline).
	// +required
	Name string `json:"name"`

	// tasks lists the tasks to dispatch in this stage.
	// Multiple tasks in a single stage execute in parallel (fan-out).
	// +required
	// +kubebuilder:validation:MinItems=1
	Tasks []PipelineTask `json:"tasks"`

	// dependsOn lists stage names that must complete before this stage starts.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
}

// PipelineTask defines a single task within a pipeline stage.
type PipelineTask struct {
	// name identifies this task (unique within the stage).
	// +required
	Name string `json:"name"`

	// agentRuntimeRef references the AgentRuntime that handles this task.
	// +required
	AgentRuntimeRef TypedReference `json:"agentRuntimeRef"`

	// skillID routes to a specific skill on the target agent.
	// +optional
	SkillID string `json:"skillID,omitempty"`

	// prompt is the task prompt template. Supports {{.PreviousStageOutput}} interpolation.
	// +optional
	Prompt string `json:"prompt,omitempty"`

	// timeout is the maximum duration for this individual task.
	// +kubebuilder:default="10m"
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// TaskPipelineStatus defines the observed state of TaskPipeline.
type TaskPipelineStatus struct {
	// conditions represent the current state of the TaskPipeline resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// stageCount is the total number of stages in this pipeline.
	// +optional
	StageCount int32 `json:"stageCount,omitempty"`

	// taskCount is the total number of tasks across all stages.
	// +optional
	TaskCount int32 `json:"taskCount,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// TaskPipeline is the Schema for the taskpipelines API
type TaskPipeline struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of TaskPipeline
	// +required
	Spec TaskPipelineSpec `json:"spec"`

	// status defines the observed state of TaskPipeline
	// +optional
	Status TaskPipelineStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TaskPipelineList contains a list of TaskPipeline
type TaskPipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TaskPipeline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TaskPipeline{}, &TaskPipelineList{})
}
