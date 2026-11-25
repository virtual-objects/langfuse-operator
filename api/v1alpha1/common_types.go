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

// LocalObjectReference references an object in the same namespace.
// Cross-namespace references are intentionally not supported, so a namespace
// can never use credentials that belong to another namespace.
type LocalObjectReference struct {
	// Name of the referenced object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// DeletionPolicy controls what happens in Langfuse when the custom resource is deleted.
// +kubebuilder:validation:Enum=Delete;Orphan
type DeletionPolicy string

const (
	// DeletionPolicyDelete removes the object from Langfuse.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyOrphan leaves the object in Langfuse.
	DeletionPolicyOrphan DeletionPolicy = "Orphan"
)

// ConditionReady is the condition type every kind reports.
const ConditionReady = "Ready"

// Reasons used on the Ready condition.
const (
	ReasonSynced             = "Synced"
	ReasonInvalidSpec        = "InvalidSpec"
	ReasonSecretNotFound     = "SecretNotFound"
	ReasonUnauthorized       = "Unauthorized"
	ReasonConnectionNotReady = "ConnectionNotReady"
	ReasonAPIError           = "APIError"
	ReasonConflict           = "Conflict"
	ReasonRejected           = "Rejected"
	ReasonEvaluated          = "Evaluated"
	ReasonSuspended          = "Suspended"
	ReasonNotificationFailed = "NotificationFailed"
)
