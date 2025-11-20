/*
Copyright 2025.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentBuildSpec defines the desired state of AgentBuild.
type AgentBuildSpec struct {
	// Source specifies the source code related configuration
	// +required
	SourceSpec SourceSpec `json:"source"`
	// Pipeline specifies the pipeline configuration
	// +optional
	Pipeline *PipelineSpec `json:"pipeline"`
	// BuildArgs are arguments to pass to the build process
	// +optional
	BuildArgs []ParameterSpec `json:"buildArgs,omitempty"`
	// BuildOutput specifies where to store build artifacts
	// +optional
	BuildOutput *BuildOutput `json:"buildOutput,omitempty"`
	// CleanupAfterBuild indicates whether to automatically cleanup after build
	// +optional
	// +kubebuilder:default=true
	CleanupAfterBuild bool `json:"cleanupAfterBuild,omitempty"`
	// Mode specifies which pipeline template to use (dev, dev-local, dev-external, preprod, prod, custom)
	// This will be used to fetch the pipeline template from ConfigMap
	// +optional
	// +kubebuilder:validation:Enum=dev;buildpack-dev;dev-local;dev-external;preprod;prod;custom
	// +kubebuilder:default=dev
	Mode string `json:"mode,omitempty"`
	// Metadata specifies labels and annotations to be added to the agent
	// +optional
	MetadataSpec `json:",inline"`
}
type SourceSpec struct {
	// SourceRepository is the Git repository URL
	// +optional
	SourceRepository string `json:"sourceRepository,omitempty"`
	// SourceRevision is the Git revision (branch, tag, commit)
	// +optional
	SourceRevision string `json:"sourceRevision,omitempty"`
	// SourceSubfolder is the folder within the repository containing the source
	// +optional
	SourceSubfolder string `json:"sourceSubfolder,omitempty"`
	// RepoUser is the username in the Git repository containing the source
	// +optional
	RepoUser string `json:"repoUser,omitempty"`
	// SourceCredentials is a reference to a secret containing Git credentials
	// +optional
	SourceCredentials *corev1.LocalObjectReference `json:"sourceCredentials,omitempty"`
}

// PipelineSpec defines how the pipeline should be configured
type PipelineSpec struct {
	// Namespace is the namespace where the pipeline steps ConfigMaps are located
	Namespace string `json:"namespace"`
	// Steps is an ordered list of pipeline steps to execute
	Steps []PipelineStepSpec `json:"steps,omitempty"`
	// Parameters contains additional parameters to pass to the pipeline
	Parameters []ParameterSpec `json:"parameters,omitempty"`
}

// PipelineStepSpec defines a single step in the pipeline
type PipelineStepSpec struct {
	// Name is the identifier for the step
	Name string `json:"name"`
	// ConfigMap references the ConfigMap containing the step definition
	ConfigMap string `json:"configMap"`
	// Enabled indicates whether this step should be included in the pipeline
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Parameters contains step-specific parameters that override global parameters
	// +optional
	Parameters []ParameterSpec `json:"parameters,omitempty"`
	// RequiredParameters lists parameter names that users must provide
	// +optional
	RequiredParameters []string `json:"requiredParameters,omitempty"`
	// WhenExpressions defines conditions for step execution
	// +optional
	WhenExpressions []WhenExpression `json:"whenExpressions,omitempty"`
}

// PipelineTemplate represents the structure stored in ConfigMaps
// This is what gets stored in the ConfigMap and merged with user parameters
type PipelineTemplate struct {
	// Metadata about the template
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Description string `json:"description"`
	// Template steps with default parameters
	Steps []PipelineStepTemplate `json:"steps"`
	// Global template parameters that apply to all steps
	// +optional
	GlobalParameters []ParameterSpec `json:"globalParameters,omitempty"`
	// Required user parameters that must be provided
	// +optional
	RequiredParameters []string `json:"requiredParameters,omitempty"`
}

// PipelineStepTemplate is a step in the template with default parameters
type PipelineStepTemplate struct {
	// Name is the identifier for the step
	Name string `json:"name"`
	// ConfigMap references the ConfigMap containing the step definition
	ConfigMap string `json:"configMap"`
	// Enabled indicates whether this step should be included by default
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`
	// DefaultParameters contains default parameters for this step
	// Users can override these by providing parameters with the same name
	// +optional
	DefaultParameters []ParameterSpec `json:"defaultParameters,omitempty"`
	// RequiredParameters lists parameter names that users must provide
	// +optional
	RequiredParameters []string `json:"requiredParameters,omitempty"`
	// Description explains what this step does
	// +optional
	Description string `json:"description,omitempty"`
	// WhenExpressions defines conditions for step execution
	// +optional
	WhenExpressions []WhenExpression `json:"whenExpressions,omitempty"`
}

// WhenExpression represents a conditional execution rule which will
// determine which build step to run (either kaniko-build or buildpack-build)
// based on the presence of a Dockerfile in the source code repository.
type WhenExpression struct {
	// Input is the value to evaluate (can reference task results)
	Input string `json:"input"`
	// Operator is the comparison operator (in, notin)
	// +kubebuilder:validation:Enum=in;notin
	Operator string `json:"operator"`
	// Values to compare against
	Values []string `json:"values"`
}

// Parameter defines an argument
type ParameterSpec struct {
	// Name of the  argument
	Name string `json:"name"`
	// Value of the argument
	Value string `json:"value"`
	// Required indicates if this parameter must be provided by the user
	// Only used in pipeline templates, not in user-provided parameters
	// +optional
	Required *bool `json:"required,omitempty"`
	// Description provides help text for the parameter
	// +optional
	Description string `json:"description,omitempty"`
}

// BuildOutput defines where to store build artifacts
type BuildOutput struct {
	// Image is the name of the image to build
	// +kubebuilder:validation:Required
	Image string `json:"image"`
	// ImageTag is the tag to apply to the built image
	// +kubebuilder:validation:Required
	ImageTag string `json:"imageTag"`
	// ImageRegistry is the container registry where the image will be pushed
	// +kubebuilder:validation:Required
	ImageRegistry string `json:"imageRegistry"`
	// ImageRepoCredentials is a reference to a secret containing registry credentials
	// +optional
	ImageRepoCredentials *corev1.LocalObjectReference `json:"imageRepoCredentials,omitempty"`
}

type LifecycleBuildPhase string

const (
	BuildPhasePending   LifecycleBuildPhase = "Pending"
	PhaseBuilding       LifecycleBuildPhase = "Building"
	BuildPhaseSucceeded LifecycleBuildPhase = "Succeeded"
	BuildPhaseFailed    LifecycleBuildPhase = "Failed"
)

// AgentBuildStatus defines the observed state of AgentBuild.
type AgentBuildStatus struct {
	// Current build phase: Pending, Building, Succeeded, Failed
	Phase LifecycleBuildPhase `json:"phase,omitempty"`
	// Build Message
	Message string `json:"message,omitempty"`
	// PipelineRun name
	PipelineRunName string `json:"pipelineRunName,omitempty"`
	// Last build time
	LastBuildTime *metav1.Time `json:"lastBuildTime,omitempty"`
	// pipeline completion time
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	BuiltImage     string       `json:"builtImage,omitempty"`
	// Conditions represent overall status
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// AgentBuild is the Schema for the Agentbuilds API.
type AgentBuild struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentBuildSpec   `json:"spec,omitempty"`
	Status AgentBuildStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentBuildList contains a list of AgentBuild.
type AgentBuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentBuild `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentBuild{}, &AgentBuildList{})
}
