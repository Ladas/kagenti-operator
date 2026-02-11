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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KagentiNamespaceSpec defines the desired state of KagentiNamespace.
type KagentiNamespaceSpec struct {
	// Prefix is prepended to the KagentiNamespace name to form the actual
	// Kubernetes namespace name. For example, if prefix is "kagenti-" and
	// the CR name is "data-science", the namespace will be "kagenti-data-science".
	// If empty, the CR name is used directly as the namespace name.
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// Secrets configures how secrets are provisioned in the namespace.
	// +optional
	Secrets *NamespaceSecrets `json:"secrets,omitempty"`

	// Keycloak configures the Keycloak client for this namespace.
	// +optional
	Keycloak *NamespaceKeycloak `json:"keycloak,omitempty"`

	// Istio configures Istio Ambient mesh settings for this namespace.
	// +optional
	Istio *NamespaceIstio `json:"istio,omitempty"`

	// Spire configures SPIRE/SPIFFE workload identity for this namespace.
	// +optional
	Spire *NamespaceSpire `json:"spire,omitempty"`
}

// NamespaceSecrets configures secret provisioning for a namespace.
type NamespaceSecrets struct {
	// InheritPlatformSecrets controls whether platform-wide secrets from
	// kagenti-system are copied into this namespace. Defaults to true.
	// +optional
	// +kubebuilder:default=true
	InheritPlatformSecrets bool `json:"inheritPlatformSecrets,omitempty"`

	// TemplateRef references a ConfigMap in kagenti-system that defines
	// which secrets to copy and the environments ConfigMap template.
	// Defaults to "kagenti-namespace-template".
	// +optional
	// +kubebuilder:default="kagenti-namespace-template"
	TemplateRef string `json:"templateRef,omitempty"`

	// Overrides allows specifying per-namespace secret overrides.
	// Each entry maps a target secret name to a source secret reference.
	// +optional
	Overrides []SecretOverride `json:"overrides,omitempty"`
}

// SecretOverride maps a target secret in the new namespace to a source secret.
type SecretOverride struct {
	// Name is the secret name in the target namespace.
	// +required
	Name string `json:"name"`

	// SourceSecret is the name of the secret to copy from.
	// +required
	SourceSecret string `json:"sourceSecret"`

	// SourceNamespace is the namespace of the source secret.
	// Defaults to kagenti-system.
	// +optional
	// +kubebuilder:default="kagenti-system"
	SourceNamespace string `json:"sourceNamespace,omitempty"`
}

// NamespaceKeycloak configures the Keycloak integration for a namespace.
type NamespaceKeycloak struct {
	// Enabled controls whether a Keycloak client is created for this namespace.
	// +optional
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// Realm is the Keycloak realm for the client. Defaults to the platform default.
	// +optional
	Realm string `json:"realm,omitempty"`
}

// NamespaceIstio configures Istio settings for a namespace.
type NamespaceIstio struct {
	// AmbientEnabled enables Istio Ambient mode for this namespace.
	// +optional
	// +kubebuilder:default=true
	AmbientEnabled bool `json:"ambientEnabled,omitempty"`

	// WaypointEnabled enables the Istio waypoint proxy for L7 policies.
	// +optional
	// +kubebuilder:default=true
	WaypointEnabled bool `json:"waypointEnabled,omitempty"`
}

// NamespaceSpire configures SPIRE/SPIFFE for a namespace.
type NamespaceSpire struct {
	// Enabled controls whether SPIRE helper config is created.
	// +optional
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// TrustDomain overrides the default SPIFFE trust domain.
	// +optional
	TrustDomain string `json:"trustDomain,omitempty"`
}

// KagentiNamespacePhase represents the provisioning phase.
// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Error
type KagentiNamespacePhase string

const (
	KagentiNamespacePhasePending      KagentiNamespacePhase = "Pending"
	KagentiNamespacePhaseProvisioning KagentiNamespacePhase = "Provisioning"
	KagentiNamespacePhaseReady        KagentiNamespacePhase = "Ready"
	KagentiNamespacePhaseError        KagentiNamespacePhase = "Error"
)

// KagentiNamespaceStatus defines the observed state of KagentiNamespace.
type KagentiNamespaceStatus struct {
	// Phase is the current provisioning phase.
	// +optional
	Phase KagentiNamespacePhase `json:"phase,omitempty"`

	// Namespace is the actual Kubernetes namespace name (prefix + CR name).
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Conditions represent the current state of provisioning.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ProvisionedAt is when the namespace was fully provisioned.
	// +optional
	ProvisionedAt *metav1.Time `json:"provisionedAt,omitempty"`
}

// Condition types for KagentiNamespace.
const (
	// ConditionNamespaceCreated indicates the Kubernetes namespace exists.
	ConditionNamespaceCreated = "NamespaceCreated"

	// ConditionSecretsProvisioned indicates all secrets have been created.
	ConditionSecretsProvisioned = "SecretsProvisioned"

	// ConditionConfigMapsProvisioned indicates ConfigMaps have been created.
	ConditionConfigMapsProvisioned = "ConfigMapsProvisioned"

	// ConditionKeycloakClientCreated indicates the Keycloak client was registered.
	ConditionKeycloakClientCreated = "KeycloakClientCreated"

	// ConditionRBACConfigured indicates RBAC bindings are in place.
	ConditionRBACConfigured = "RBACConfigured"

	// ConditionSpireConfigured indicates SPIRE config is created.
	ConditionSpireConfigured = "SpireConfigured"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=kns
// +kubebuilder:printcolumn:name="Namespace",type="string",JSONPath=".status.namespace",description="Kubernetes Namespace"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Provisioning Phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KagentiNamespace is the Schema for the kagentinamespaces API.
// It provisions a Kubernetes namespace with all resources required for
// Kagenti agent deployment: secrets, ConfigMaps, RBAC, Istio labels,
// SPIRE configuration, and Keycloak client registration.
type KagentiNamespace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KagentiNamespaceSpec   `json:"spec,omitempty"`
	Status KagentiNamespaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KagentiNamespaceList contains a list of KagentiNamespace.
type KagentiNamespaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KagentiNamespace `json:"items"`
}

// GetNamespaceName returns the actual Kubernetes namespace name
// by combining the prefix with the CR name.
func (kns *KagentiNamespace) GetNamespaceName() string {
	if kns.Spec.Prefix != "" {
		return kns.Spec.Prefix + kns.Name
	}
	return kns.Name
}

func init() {
	SchemeBuilder.Register(&KagentiNamespace{}, &KagentiNamespaceList{})
}
