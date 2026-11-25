/*
Copyright 2026 The langfuse-operator Authors.

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

// SecretKeyRef selects the Secret holding a Langfuse project API key pair.
type SecretKeyRef struct {
	// Name of the Secret, in the same namespace as the LangfuseConnection.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// PublicKeyKey is the Secret data key that holds the public key (pk-lf-...).
	// +kubebuilder:default=public_key
	// +optional
	PublicKeyKey string `json:"publicKeyKey,omitempty"`

	// SecretKeyKey is the Secret data key that holds the secret key (sk-lf-...).
	// +kubebuilder:default=secret_key
	// +optional
	SecretKeyKey string `json:"secretKeyKey,omitempty"`
}

// LangfuseConnectionSpec defines how to reach one Langfuse project.
type LangfuseConnectionSpec struct {
	// Host is the base URL of Langfuse, e.g. http://langfuse-web.langfuse.svc:3000.
	// +kubebuilder:validation:Pattern=`^https?://[^\s/]+(/\S*)?$`
	// +kubebuilder:validation:MaxLength=2048
	Host string `json:"host"`

	// APIKeySecretRef points to a Secret with a project-scoped API key pair.
	// The Secret can be created by any means, for example External Secrets Operator.
	APIKeySecretRef SecretKeyRef `json:"apiKeySecretRef"`
}

// LangfuseConnectionStatus defines the observed state of LangfuseConnection.
type LangfuseConnectionStatus struct {
	// ProjectID is the Langfuse project the API key belongs to.
	// +optional
	ProjectID string `json:"projectID,omitempty"`

	// ProjectName is the name of that project.
	// +optional
	ProjectName string `json:"projectName,omitempty"`

	// ObservedGeneration is the last spec generation that was reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions describe the current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lfconn,categories=langfuse
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.spec.host`
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.status.projectName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LangfuseConnection is a reusable reference to one Langfuse project: a host
// plus a Secret with that project's API key pair. Other kinds point at it
// with connectionRef.
type LangfuseConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LangfuseConnectionSpec   `json:"spec"`
	Status LangfuseConnectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LangfuseConnectionList contains a list of LangfuseConnection.
type LangfuseConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LangfuseConnection `json:"items"`
}

func init() {
	register(&LangfuseConnection{}, &LangfuseConnectionList{})
}
