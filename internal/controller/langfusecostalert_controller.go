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

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/costalert"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
	"github.com/virtual-objects/langfuse-operator/internal/notify"
)

const (
	indexCostAlertConnection = "costalert.spec.connectionRef.name"
	labelNamespace           = "namespace"
	labelName                = "name"
	labelRule                = "rule"
)

var (
	costRuleValue = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "langfuse_cost_alert_rule_value",
		Help: "Last measured value of a LangfuseCostAlert rule (USD, USD per trace, or unpriced tokens).",
	}, []string{labelNamespace, labelName, labelRule})
	costRuleFiring = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "langfuse_cost_alert_rule_firing",
		Help: "1 when a LangfuseCostAlert rule is firing, otherwise 0.",
	}, []string{labelNamespace, labelName, labelRule})
	costNotifications = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "langfuse_cost_alert_notifications_total",
		Help: "Notifications sent by LangfuseCostAlerts, by type and result.",
	}, []string{labelNamespace, labelName, "type", "result"})
)

func init() {
	metrics.Registry.MustRegister(costRuleValue, costRuleFiring, costNotifications)
}

// SenderFactory builds a notification sender from a Slack webhook URL.
type SenderFactory func(webhookURL string) (notify.Sender, error)

// DefaultSenderFactory sends to Slack.
func DefaultSenderFactory(webhookURL string) (notify.Sender, error) {
	return notify.NewSlack(webhookURL, langfuse.ClientName+"/"+langfuse.Version)
}

// LangfuseCostAlertReconciler evaluates cost rules and sends notifications.
type LangfuseCostAlertReconciler struct {
	client.Client
	Resolver  *ConnectionResolver
	Recorder  events.EventRecorder
	NewSender SenderFactory
	// Now is the clock; tests may replace it.
	Now func() time.Time
	// NotifyRetry is how soon a failed notification is retried. Defaults to 30s.
	NotifyRetry time.Duration
}

// +kubebuilder:rbac:groups=langfuse.virtual-objects.github.io,resources=langfusecostalerts,verbs=get;list;watch
// +kubebuilder:rbac:groups=langfuse.virtual-objects.github.io,resources=langfusecostalerts/status,verbs=get;update;patch

// Reconcile evaluates the alert when it is due.
func (r *LangfuseCostAlertReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var alert v1.LangfuseCostAlert
	if err := r.Get(ctx, req.NamespacedName, &alert); err != nil {
		if apierrors.IsNotFound(err) {
			r.forgetMetrics(req.Namespace, req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	now := r.now()

	// Evaluate at most once per interval, unless the spec changed or a
	// notification has to be retried.
	interval := costalert.Interval(&alert.Spec)
	if c := meta.FindStatusCondition(alert.Status.Conditions, v1.ConditionReady); c != nil &&
		c.Reason == v1.ReasonNotificationFailed {
		interval = r.notifyRetry()
	}
	if last := alert.Status.LastEvaluationTime; last != nil && alert.Status.ObservedGeneration == alert.Generation {
		if wait := last.Add(interval).Sub(now); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	orig := alert.DeepCopy()
	result, evalErr := r.evaluate(ctx, &alert, now)
	alert.Status.ObservedGeneration = alert.Generation
	if !equality.Semantic.DeepEqual(orig.Status, alert.Status) {
		if err := r.Status().Patch(ctx, &alert, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status: %w", err)
		}
	}
	return result, evalErr
}

func (r *LangfuseCostAlertReconciler) notifyRetry() time.Duration {
	if r.NotifyRetry > 0 {
		return r.NotifyRetry
	}
	return retryShort
}

func (r *LangfuseCostAlertReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *LangfuseCostAlertReconciler) notReady(a *v1.LangfuseCostAlert, reason, msg string) {
	setReady(&a.Status.Conditions, a.Generation, false, reason, msg)
}

func (r *LangfuseCostAlertReconciler) evaluate(ctx context.Context, a *v1.LangfuseCostAlert, now time.Time) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if a.Spec.Suspend {
		r.notReady(a, v1.ReasonSuspended, "evaluation is suspended")
		return ctrl.Result{}, nil
	}
	if err := costalert.Validate(&a.Spec); err != nil {
		r.notReady(a, v1.ReasonInvalidSpec, err.Error())
		return ctrl.Result{}, nil
	}

	conn, lf, msg, err := readyConnection(ctx, r.Client, r.Resolver, a.Namespace, a.Spec.ConnectionRef.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if lf == nil {
		r.notReady(a, v1.ReasonConnectionNotReady, msg)
		return ctrl.Result{RequeueAfter: retryLong}, nil
	}

	sender, err := r.sender(ctx, a)
	if err != nil {
		r.notReady(a, v1.ReasonSecretNotFound, err.Error())
		return ctrl.Result{RequeueAfter: retryShort}, nil
	}

	end := now.Add(-costalert.IngestionDelay(&a.Spec))
	filter := costalert.Filter(a.Spec.Agent)
	repeat := costalert.RepeatInterval(&a.Spec)
	sendResolved := costalert.SendResolved(&a.Spec)
	prev := map[string]*v1.CostAlertRuleStatus{}
	for i := range a.Status.Rules {
		prev[a.Status.Rules[i].Name] = &a.Status.Rules[i]
	}

	r.forgetMetrics(a.Namespace, a.Name)
	var statuses []v1.CostAlertRuleStatus
	var firing int32
	var ruleErrors, notifyErrors []string
	for i := range a.Spec.Rules {
		rule := &a.Spec.Rules[i]
		res := costalert.Evaluate(ctx, lf, filter, rule, end)
		st, err := costalert.Apply(prev[rule.Name], rule.Name, res, now, repeat, sendResolved,
			func(act costalert.Action) error {
				return r.send(ctx, sender, a, conn, rule, res, prev[rule.Name], act, now)
			})
		if err != nil {
			notifyErrors = append(notifyErrors, fmt.Sprintf("%s: %v", rule.Name, err))
		}
		if res.State == v1.RuleError {
			ruleErrors = append(ruleErrors, fmt.Sprintf("%s: %s", rule.Name, res.Message))
		}
		if st.FiringSince != nil {
			firing++
		}
		statuses = append(statuses, st)

		labels := prometheus.Labels{labelNamespace: a.Namespace, labelName: a.Name, labelRule: rule.Name}
		if res.State != v1.RuleError && res.State != v1.RuleNoData {
			costRuleValue.With(labels).Set(res.Number)
		}
		f := 0.0
		if st.FiringSince != nil {
			f = 1
		}
		costRuleFiring.With(labels).Set(f)
	}

	t := metav1.NewTime(now)
	a.Status.Rules = statuses
	a.Status.FiringRules = firing
	a.Status.LastEvaluationTime = &t
	a.Status.ProjectID = conn.Status.ProjectID

	switch {
	case len(notifyErrors) > 0:
		log.Info("notification failed", "errors", notifyErrors)
		r.notReady(a, v1.ReasonNotificationFailed, strings.Join(notifyErrors, "; "))
		return ctrl.Result{RequeueAfter: r.notifyRetry()}, nil
	case len(ruleErrors) > 0:
		r.notReady(a, v1.ReasonAPIError, strings.Join(ruleErrors, "; "))
	default:
		setReady(&a.Status.Conditions, a.Generation, true, v1.ReasonEvaluated,
			fmt.Sprintf("%d rules evaluated, %d firing", len(statuses), firing))
	}
	return ctrl.Result{RequeueAfter: costalert.Interval(&a.Spec)}, nil
}

func (r *LangfuseCostAlertReconciler) sender(ctx context.Context, a *v1.LangfuseCostAlert) (notify.Sender, error) {
	ref := a.Spec.Notification.Slack.WebhookSecretRef
	key := ref.Key
	if key == "" {
		key = "webhook_url"
	}
	var secret corev1.Secret
	if err := r.Resolver.SecretReader.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: ref.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("slack webhook secret %q not found", ref.Name)
		}
		return nil, fmt.Errorf("get secret %q: %w", ref.Name, err)
	}
	webhook := string(secret.Data[key])
	if webhook == "" {
		return nil, fmt.Errorf("secret %q has no non-empty key %q", ref.Name, key)
	}
	factory := r.NewSender
	if factory == nil {
		factory = DefaultSenderFactory
	}
	return factory(webhook)
}

func agentLabel(a *v1.LangfuseCostAlert) string {
	if a.Spec.Agent.TraceName != "" {
		return a.Spec.Agent.TraceName
	}
	return "all traces"
}

func ruleDescription(rule *v1.CostAlertRule) string {
	switch {
	case rule.Budget != nil:
		return fmt.Sprintf("budget %s per %s", rule.Budget.MaxCostUSD, rule.Budget.Window.Duration)
	case rule.Change != nil:
		var parts []string
		if rule.Change.IncreasePercent != nil {
			parts = append(parts, fmt.Sprintf("+%d%%", *rule.Change.IncreasePercent))
		}
		if rule.Change.DecreasePercent != nil {
			parts = append(parts, fmt.Sprintf("-%d%%", *rule.Change.DecreasePercent))
		}
		return fmt.Sprintf("cost change %s over %s", strings.Join(parts, " / "), rule.Change.Window.Duration)
	case rule.CostPerTrace != nil:
		return fmt.Sprintf("cost per trace +%d%% over %s", rule.CostPerTrace.IncreasePercent, rule.CostPerTrace.Window.Duration)
	case rule.UnpricedUsage != nil:
		return fmt.Sprintf("unpriced usage over %s", rule.UnpricedUsage.Window.Duration)
	}
	return ""
}

func (r *LangfuseCostAlertReconciler) send(ctx context.Context, sender notify.Sender, a *v1.LangfuseCostAlert,
	conn *v1.LangfuseConnection, rule *v1.CostAlertRule, res costalert.Result, prev *v1.CostAlertRuleStatus,
	act costalert.Action, now time.Time) error {
	resolved := act == costalert.Resolved
	summary := res.Message
	fields := []notify.Field{
		{Label: "Project", Value: conn.Status.ProjectName},
		{Label: "Agent", Value: agentLabel(a)},
	}
	if a.Spec.Agent.Environment != "" {
		fields = append(fields, notify.Field{Label: "Environment", Value: a.Spec.Agent.Environment})
	}
	fields = append(fields, notify.Field{Label: "Rule", Value: ruleDescription(rule)})
	if res.Value != "" {
		fields = append(fields, notify.Field{Label: "Current", Value: res.Value})
	}
	if res.Baseline != "" {
		fields = append(fields, notify.Field{Label: "Compared with", Value: res.Baseline})
	}
	if prev != nil && prev.FiringSince != nil {
		fields = append(fields, notify.Field{Label: "Firing since", Value: prev.FiringSince.UTC().Format(time.RFC3339)})
	}
	base := conn.Spec.UIURL
	if base == "" {
		base = conn.Spec.Host
	}
	m := &notify.Message{
		Resolved:   resolved,
		Title:      fmt.Sprintf("%s: %s", agentLabel(a), rule.Name),
		Summary:    summary,
		Fields:     fields,
		LinkURL:    strings.TrimRight(base, "/") + "/project/" + conn.Status.ProjectID + "/traces",
		LinkText:   "Open traces in Langfuse",
		Mentions:   a.Spec.Notification.Slack.Mentions,
		Source:     a.Namespace + "/" + a.Name,
		OccurredAt: now,
	}
	kind := "firing"
	if resolved {
		kind = "resolved"
	}
	err := sender.Send(ctx, m)
	result := "success"
	if err != nil {
		result = "error"
	}
	costNotifications.With(prometheus.Labels{labelNamespace: a.Namespace, labelName: a.Name, "type": kind, "result": result}).Inc()
	if err != nil {
		return err
	}
	if resolved {
		r.Recorder.Eventf(a, nil, corev1.EventTypeNormal, "Resolved", "Notify", "%s resolved: %s", rule.Name, summary)
	} else {
		r.Recorder.Eventf(a, nil, corev1.EventTypeWarning, "Firing", "Notify", "%s firing: %s", rule.Name, summary)
	}
	return nil
}

func (r *LangfuseCostAlertReconciler) forgetMetrics(namespace, name string) {
	l := prometheus.Labels{labelNamespace: namespace, labelName: name}
	costRuleValue.DeletePartialMatch(l)
	costRuleFiring.DeletePartialMatch(l)
}

// SetupWithManager sets up the controller with the Manager.
func (r *LangfuseCostAlertReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1.LangfuseCostAlert{}, indexCostAlertConnection,
		func(o client.Object) []string {
			return []string{o.(*v1.LangfuseCostAlert).Spec.ConnectionRef.Name}
		}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.LangfuseCostAlert{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1.LangfuseConnection{}, handler.EnqueueRequestsFromMapFunc(r.alertsForConnection)).
		Named("langfusecostalert").
		Complete(r)
}

func (r *LangfuseCostAlertReconciler) alertsForConnection(ctx context.Context, o client.Object) []reconcile.Request {
	var list v1.LangfuseCostAlertList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace()),
		client.MatchingFields{indexCostAlertConnection: o.GetName()}); err != nil {
		logf.FromContext(ctx).Error(err, "list cost alerts for connection", "connection", o.GetName())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, a := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
	}
	return reqs
}
