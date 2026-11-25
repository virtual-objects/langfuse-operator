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

package costalert

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
)

// Action is the notification an evaluation calls for.
type Action int

const (
	// None means nothing is sent.
	None Action = iota
	// Firing announces a new or still-firing rule.
	Firing
	// Resolved announces that a firing rule recovered.
	Resolved
)

// Notifier sends one notification; Apply only commits the new state when it succeeds.
type Notifier func(Action) error

// Apply turns the previous status and a fresh result into the new status.
// FiringSince marks an alert as active: it survives Error and NoData results,
// so a flaky Langfuse never produces a false "resolved". When notify fails, the
// notification state is kept so the next evaluation retries it.
func Apply(prev *v1.CostAlertRuleStatus, name string, res Result, now time.Time, repeat time.Duration,
	sendResolved bool, notify Notifier) (v1.CostAlertRuleStatus, error) {
	next := v1.CostAlertRuleStatus{
		Name: name, State: res.State, Value: res.Value, Baseline: res.Baseline, Message: res.Message,
	}
	var firingSince, lastNotified *metav1.Time
	if prev != nil {
		firingSince, lastNotified = prev.FiringSince, prev.LastNotified
	}
	t := metav1.NewTime(now)

	switch res.State {
	case v1.RuleFiring:
		action := None
		if firingSince == nil {
			firingSince = &t
			lastNotified = nil
		}
		if lastNotified == nil || now.Sub(lastNotified.Time) >= repeat {
			action = Firing
		}
		next.FiringSince = firingSince
		next.LastNotified = lastNotified
		if action == Firing {
			if err := notify(Firing); err != nil {
				return next, err
			}
			next.LastNotified = &t
		}
		return next, nil

	case v1.RuleOK:
		if firingSince == nil {
			return next, nil
		}
		if sendResolved {
			if err := notify(Resolved); err != nil {
				next.FiringSince, next.LastNotified = firingSince, lastNotified
				return next, err
			}
		}
		return next, nil

	default: // NoData, Error: keep whatever alert is active
		next.FiringSince, next.LastNotified = firingSince, lastNotified
		return next, nil
	}
}
