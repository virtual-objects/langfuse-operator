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

// AgentSelector selects the traces that belong to one agent within the
// connection's Langfuse project. All set fields must match. An empty selector
// selects the whole project.
type AgentSelector struct {
	// TraceName is the Langfuse trace name the agent uses, e.g. "rca-agent".
	// +kubebuilder:validation:MaxLength=512
	// +optional
	TraceName string `json:"traceName,omitempty"`

	// Environment is the Langfuse environment, e.g. "production".
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Environment string `json:"environment,omitempty"`

	// Tags that every selected trace must carry.
	// +kubebuilder:validation:MaxItems=10
	// +listType=set
	// +optional
	Tags []string `json:"tags,omitempty"`
}

// BudgetRule fires when the agent's cost in the window exceeds a budget.
type BudgetRule struct {
	// Window is the period the cost is summed over, e.g. 1h or 24h.
	Window metav1.Duration `json:"window"`

	// MaxCostUSD is the budget for one window.
	MaxCostUSD Price `json:"maxCostUSD"`
}

// ChangeRule fires when the agent's cost in the window moved by more than a
// percentage compared with the same-length window CompareOffset earlier.
// Use it to catch spikes (increasePercent) and drops such as a stopped agent
// (decreasePercent).
// +kubebuilder:validation:XValidation:rule="has(self.increasePercent) || has(self.decreasePercent)",message="set increasePercent, decreasePercent, or both"
type ChangeRule struct {
	// Window is the period the cost is summed over.
	Window metav1.Duration `json:"window"`

	// CompareOffset is how far back the comparison window starts. Defaults to
	// Window (the previous window). Use 168h for week-over-week.
	// +optional
	CompareOffset *metav1.Duration `json:"compareOffset,omitempty"`

	// IncreasePercent fires when cost rose by at least this much, e.g. 50.
	// +kubebuilder:validation:Minimum=1
	// +optional
	IncreasePercent *int32 `json:"increasePercent,omitempty"`

	// DecreasePercent fires when cost fell by at least this much, e.g. 90.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +optional
	DecreasePercent *int32 `json:"decreasePercent,omitempty"`

	// MinCostUSD ignores windows where both values are below this cost, so
	// small absolute changes do not page anyone.
	// +optional
	MinCostUSD Price `json:"minCostUSD,omitempty"`
}

// CostPerTraceRule fires when the average cost of one agent run (trace) rose,
// e.g. after a model switch or a growing prompt, even if traffic did not change.
type CostPerTraceRule struct {
	// Window is the period the average is computed over.
	Window metav1.Duration `json:"window"`

	// CompareOffset is how far back the comparison window starts. Defaults to Window.
	// +optional
	CompareOffset *metav1.Duration `json:"compareOffset,omitempty"`

	// IncreasePercent fires when the average cost per trace rose by at least this much.
	// +kubebuilder:validation:Minimum=1
	IncreasePercent int32 `json:"increasePercent"`

	// MinTraces is the minimum number of traces in both windows for a
	// meaningful average.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinTraces int64 `json:"minTraces,omitempty"`
}

// UnpricedUsageRule fires when generations used tokens but got no cost,
// usually because no model definition matches their model ID.
type UnpricedUsageRule struct {
	// Window is the period to look at.
	Window metav1.Duration `json:"window"`

	// MinTokens is the number of unpriced tokens per model that triggers the rule.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinTokens int64 `json:"minTokens,omitempty"`
}

// CostAlertRule is one condition to evaluate. Set exactly one rule type.
// +kubebuilder:validation:XValidation:rule="[has(self.budget), has(self.change), has(self.costPerTrace), has(self.unpricedUsage)].filter(x, x).size() == 1",message="set exactly one of budget, change, costPerTrace, unpricedUsage"
type CostAlertRule struct {
	// Name identifies the rule in notifications and status.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// +optional
	Budget *BudgetRule `json:"budget,omitempty"`
	// +optional
	Change *ChangeRule `json:"change,omitempty"`
	// +optional
	CostPerTrace *CostPerTraceRule `json:"costPerTrace,omitempty"`
	// +optional
	UnpricedUsage *UnpricedUsageRule `json:"unpricedUsage,omitempty"`
}

// SecretValueRef selects one key of a Secret in the same namespace.
type SecretValueRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Key in the Secret.
	// +kubebuilder:default=webhook_url
	// +optional
	Key string `json:"key,omitempty"`
}

// SlackNotification sends messages to a Slack incoming webhook.
type SlackNotification struct {
	// WebhookSecretRef points to the Secret key that holds the webhook URL
	// (https://hooks.slack.com/services/...). The URL is a credential: keep it
	// in a Secret, for example delivered by External Secrets Operator.
	WebhookSecretRef SecretValueRef `json:"webhookSecretRef"`

	// Mentions are added to firing messages, e.g. "<!subteam^S0123ABC>" or "<@U0123ABC>".
	// +kubebuilder:validation:MaxItems=10
	// +optional
	Mentions []string `json:"mentions,omitempty"`
}

// CostAlertNotification configures where and how often alerts are sent.
type CostAlertNotification struct {
	// Slack target.
	Slack SlackNotification `json:"slack"`

	// RepeatInterval re-sends a still-firing alert after this long.
	// +kubebuilder:default="6h"
	// +optional
	RepeatInterval *metav1.Duration `json:"repeatInterval,omitempty"`

	// SendResolved sends a message when a firing rule recovers.
	// +kubebuilder:default=true
	// +optional
	SendResolved *bool `json:"sendResolved,omitempty"`
}

// LangfuseCostAlertSpec defines cost rules for one agent.
type LangfuseCostAlertSpec struct {
	// ConnectionRef names the LangfuseConnection (same namespace) for the project.
	ConnectionRef LocalObjectReference `json:"connectionRef"`

	// Agent selects the traces to watch.
	// +optional
	Agent AgentSelector `json:"agent,omitempty"`

	// Rules to evaluate.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=20
	// +listType=map
	// +listMapKey=name
	Rules []CostAlertRule `json:"rules"`

	// Notification target.
	Notification CostAlertNotification `json:"notification"`

	// Interval between evaluations.
	// +kubebuilder:default="15m"
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`

	// IngestionDelay shifts every window back in time so that data still being
	// ingested does not look like a drop in cost.
	// +kubebuilder:default="5m"
	// +optional
	IngestionDelay *metav1.Duration `json:"ingestionDelay,omitempty"`

	// Suspend stops evaluation and notifications.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// RuleState is the evaluation state of one rule.
// +kubebuilder:validation:Enum=OK;Firing;NoData;Error
type RuleState string

// Rule states.
const (
	RuleOK     RuleState = "OK"
	RuleFiring RuleState = "Firing"
	RuleNoData RuleState = "NoData"
	RuleError  RuleState = "Error"
)

// CostAlertRuleStatus is the last evaluation of one rule.
type CostAlertRuleStatus struct {
	// Name of the rule.
	Name string `json:"name"`

	// State after the last evaluation.
	State RuleState `json:"state"`

	// Value is the measured value (USD, USD per trace, or unpriced tokens).
	// +optional
	Value string `json:"value,omitempty"`

	// Baseline is the value it was compared with, if any.
	// +optional
	Baseline string `json:"baseline,omitempty"`

	// Message describes the result.
	// +optional
	Message string `json:"message,omitempty"`

	// FiringSince is when the rule started firing.
	// +optional
	FiringSince *metav1.Time `json:"firingSince,omitempty"`

	// LastNotified is when a notification for the current state was last sent.
	// +optional
	LastNotified *metav1.Time `json:"lastNotified,omitempty"`
}

// LangfuseCostAlertStatus defines the observed state of LangfuseCostAlert.
type LangfuseCostAlertStatus struct {
	// ProjectID of the watched Langfuse project.
	// +optional
	ProjectID string `json:"projectID,omitempty"`

	// LastEvaluationTime is when the rules were last evaluated.
	// +optional
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`

	// FiringRules is the number of rules currently firing.
	// +optional
	FiringRules int32 `json:"firingRules,omitempty"`

	// Rules holds the result per rule.
	// +listType=map
	// +listMapKey=name
	// +optional
	Rules []CostAlertRuleStatus `json:"rules,omitempty"`

	// ObservedGeneration is the last spec generation that was evaluated.
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
// +kubebuilder:resource:shortName=lfcostalert,categories=langfuse
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agent.traceName`
// +kubebuilder:printcolumn:name="Firing",type=integer,JSONPath=`.status.firingRules`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1
// +kubebuilder:printcolumn:name="Last eval",type=date,JSONPath=`.status.lastEvaluationTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LangfuseCostAlert watches the LLM cost of one agent in a Langfuse project and
// sends Slack notifications when a rule fires or recovers.
type LangfuseCostAlert struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LangfuseCostAlertSpec   `json:"spec"`
	Status LangfuseCostAlertStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LangfuseCostAlertList contains a list of LangfuseCostAlert.
type LangfuseCostAlertList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LangfuseCostAlert `json:"items"`
}

func init() {
	register(&LangfuseCostAlert{}, &LangfuseCostAlertList{})
}
