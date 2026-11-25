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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModelUnit is the usage unit a model is priced in.
// +kubebuilder:validation:Enum=TOKENS;CHARACTERS;MILLISECONDS;SECONDS;REQUESTS;IMAGES
type ModelUnit string

// Price is a non-negative decimal in USD, written as a string to avoid float rounding.
// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
// +kubebuilder:validation:MaxLength=40
type Price string

// ConditionOperator compares a usage value against a threshold.
// +kubebuilder:validation:Enum=gt;gte;lt;lte;eq;neq
type ConditionOperator string

// PricingTierCondition selects a tier from the usage details of a generation,
// for example "input tokens greater than 200000".
type PricingTierCondition struct {
	// UsageDetailPattern is a regex matched against usage detail keys, e.g. "input".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	UsageDetailPattern string `json:"usageDetailPattern"`

	// Operator used to compare the summed usage with Value.
	Operator ConditionOperator `json:"operator"`

	// Value is the threshold.
	// +kubebuilder:validation:Minimum=0
	Value int64 `json:"value"`

	// CaseSensitive controls regex matching of UsageDetailPattern.
	// +optional
	CaseSensitive bool `json:"caseSensitive,omitempty"`
}

// PricingTier is a set of prices that applies when its conditions match.
// Every model needs exactly one default tier (isDefault: true, priority 0, no conditions).
// +kubebuilder:validation:XValidation:rule="(has(self.pricePerMillion) && size(self.pricePerMillion) > 0) || (has(self.pricePerUnit) && size(self.pricePerUnit) > 0)",message="a pricing tier needs at least one price in pricePerMillion or pricePerUnit"
type PricingTier struct {
	// Name of the tier, unique within the model.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// IsDefault marks the fallback tier. Exactly one tier must be the default.
	// +optional
	IsDefault bool `json:"isDefault,omitempty"`

	// Priority orders tier evaluation (lower first). The default tier must use 0.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=999
	// +optional
	Priority int32 `json:"priority,omitempty"`

	// Conditions that select this tier. Must be empty for the default tier and
	// non-empty for every other tier.
	// +kubebuilder:validation:MaxItems=10
	// +listType=atomic
	// +optional
	Conditions []PricingTierCondition `json:"conditions,omitempty"`

	// PricePerMillion holds prices in USD per 1,000,000 units.
	// Keys are either aliases, which expand to every usage-type name Langfuse and
	// its SDKs report, or literal Langfuse usage types, which are used as-is:
	//   input      -> input, input_tokens
	//   output     -> output, output_tokens
	//   cacheRead  -> input_cache_read, cache_read_input_tokens
	//   cacheWrite -> input_cache_creation, cache_creation_input_tokens
	//   total      -> total
	// +kubebuilder:validation:MaxProperties=50
	// +optional
	PricePerMillion map[string]Price `json:"pricePerMillion,omitempty"`

	// PricePerUnit holds prices in USD per single unit, passed to Langfuse 1:1.
	// Keys follow the same alias rules as PricePerMillion. A usage type set here
	// overrides the same usage type derived from PricePerMillion.
	// +kubebuilder:validation:MaxProperties=50
	// +optional
	PricePerUnit map[string]Price `json:"pricePerUnit,omitempty"`
}

// LangfuseModelSpec defines a custom model definition in one Langfuse project.
// +kubebuilder:validation:XValidation:rule="self.pricingTiers.filter(t, has(t.isDefault) && t.isDefault).size() == 1",message="exactly one pricing tier must have isDefault: true"
// +kubebuilder:validation:XValidation:rule="self.pricingTiers.all(t, !(has(t.isDefault) && t.isDefault) || ((!has(t.priority) || t.priority == 0) && (!has(t.conditions) || size(t.conditions) == 0)))",message="the default pricing tier must have priority 0 and no conditions"
// +kubebuilder:validation:XValidation:rule="self.pricingTiers.all(t, (has(t.isDefault) && t.isDefault) || (has(t.conditions) && size(t.conditions) > 0))",message="every non-default pricing tier needs at least one condition"
// +kubebuilder:validation:XValidation:rule="self.pricingTiers.all(a, self.pricingTiers.exists_one(b, (has(b.priority) ? b.priority : 0) == (has(a.priority) ? a.priority : 0)))",message="pricing tier priorities must be unique"
type LangfuseModelSpec struct {
	// ConnectionRef names the LangfuseConnection (same namespace) for the target project.
	ConnectionRef LocalObjectReference `json:"connectionRef"`

	// ModelName is the model name in Langfuse, unique within the project.
	// A project model with the same name as a built-in Langfuse model overrides it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	ModelName string `json:"modelName"`

	// MatchPattern is a PostgreSQL regular expression matched against the
	// model ID that generations report, e.g. (?i)^(anthropic\.)?claude-sonnet-4-6$.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	MatchPattern string `json:"matchPattern"`

	// Unit the prices are expressed in.
	// +kubebuilder:default=TOKENS
	// +optional
	Unit ModelUnit `json:"unit,omitempty"`

	// TokenizerID lets Langfuse count tokens when a generation reports none.
	// +kubebuilder:validation:Enum=openai;claude
	// +optional
	TokenizerID string `json:"tokenizerId,omitempty"`

	// TokenizerConfig is passed to the tokenizer, e.g. {"tokensPerMessage": 3}.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	TokenizerConfig *apiextensionsv1.JSON `json:"tokenizerConfig,omitempty"`

	// PricingTiers of the model. Usage types must be the same in every tier.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	// +listType=map
	// +listMapKey=name
	PricingTiers []PricingTier `json:"pricingTiers"`

	// DeletionPolicy decides whether deleting this resource also deletes the
	// model in Langfuse.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// LangfuseModelStatus defines the observed state of LangfuseModel.
type LangfuseModelStatus struct {
	// ModelID is the ID of the model in Langfuse.
	// +optional
	ModelID string `json:"modelID,omitempty"`

	// ProjectID is the Langfuse project that holds the model.
	// +optional
	ProjectID string `json:"projectID,omitempty"`

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
// +kubebuilder:resource:shortName=lfmodel,categories=langfuse
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.modelName`
// +kubebuilder:printcolumn:name="Connection",type=string,JSONPath=`.spec.connectionRef.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Model ID",type=string,JSONPath=`.status.modelID`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LangfuseModel is a custom model definition with pricing in a Langfuse project.
type LangfuseModel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LangfuseModelSpec   `json:"spec"`
	Status LangfuseModelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LangfuseModelList contains a list of LangfuseModel.
type LangfuseModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LangfuseModel `json:"items"`
}

func init() {
	register(&LangfuseModel{}, &LangfuseModelList{})
}
