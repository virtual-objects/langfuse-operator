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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse/fake"
)

// slackCapture is a fake Slack incoming webhook. Paths ending in /fail return 500.
type slackCapture struct {
	*httptest.Server
	mu   sync.Mutex
	msgs map[string][]string // path -> message texts
}

func newSlackCapture() *slackCapture {
	s := &slackCapture{msgs: map[string][]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/fail") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var m struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(b, &m)
		s.mu.Lock()
		s.msgs[r.URL.Path] = append(s.msgs[r.URL.Path], m.Text)
		s.mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	return s
}

func (s *slackCapture) messages(path string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.msgs[path]...)
}

func cost(v float64) *float64 { return &v }

func (e env) createWebhookSecret(path string) {
	Expect(k8sClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "slack"},
		StringData: map[string]string{"webhook_url": slackServer.URL + path},
	})).To(Succeed())
}

func dur(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

func costAlert(e env, name string, rules ...v1.CostAlertRule) *v1.LangfuseCostAlert {
	return &v1.LangfuseCostAlert{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: name},
		Spec: v1.LangfuseCostAlertSpec{
			ConnectionRef:  v1.LocalObjectReference{Name: "lf"},
			Agent:          v1.AgentSelector{TraceName: "rca-agent", Environment: "production"},
			Rules:          rules,
			IngestionDelay: dur(0),
			Notification: v1.CostAlertNotification{Slack: v1.SlackNotification{
				WebhookSecretRef: v1.SecretValueRef{Name: "slack"},
				Mentions:         []string{"<!subteam^S1>"},
			}},
		},
	}
}

func budgetRule(maxUSD string) v1.CostAlertRule {
	return v1.CostAlertRule{Name: "hourly-budget",
		Budget: &v1.BudgetRule{Window: metav1.Duration{Duration: time.Hour}, MaxCostUSD: v1.Price(maxUSD)}}
}

func (e env) seed(name, envName string, ago time.Duration, gens ...fake.Generation) {
	lfServer.AddTrace(e.projectID, fake.Trace{Name: name, Environment: envName, Timestamp: time.Now().Add(-ago), Generations: gens})
}

// ruleStatus returns the status of one rule of the alert named "rca".
func ruleStatus(e env, rule string) v1.CostAlertRuleStatus {
	GinkgoHelper()
	var a v1.LangfuseCostAlert
	Expect(k8sClient.Get(ctx, e.key("rca"), &a)).To(Succeed())
	for _, r := range a.Status.Rules {
		if r.Name == rule {
			return r
		}
	}
	return v1.CostAlertRuleStatus{}
}

var _ = Describe("LangfuseCostAlert", func() {
	It("fires on a budget breach, notifies Slack once, and resolves", func() {
		e := newEnv(true)
		path := "/services/" + e.ns
		e.createWebhookSecret(path)
		e.seed("rca-agent", "production", 10*time.Minute, fake.Generation{Model: "sonnet", Tokens: 1000, CostUSD: cost(7)})
		e.seed("rca-agent", "production", 20*time.Minute, fake.Generation{Model: "sonnet", Tokens: 1000, CostUSD: cost(6)})
		// Other agents and environments must not count.
		e.seed("chat-agent", "production", 5*time.Minute, fake.Generation{Model: "sonnet", Tokens: 1, CostUSD: cost(100)})
		e.seed("rca-agent", "staging", 5*time.Minute, fake.Generation{Model: "sonnet", Tokens: 1, CostUSD: cost(100)})

		Expect(k8sClient.Create(ctx, costAlert(e, "rca", budgetRule("10")))).To(Succeed())
		Eventually(func() []string { return slackServer.messages(path) }, timeout, interval).Should(HaveLen(1))
		msg := slackServer.messages(path)[0]
		Expect(msg).To(HavePrefix("<!subteam^S1> [FIRING] rca-agent: hourly-budget"))
		Expect(msg).To(ContainSubstring("$13.0000"))

		var a v1.LangfuseCostAlert
		Expect(k8sClient.Get(ctx, e.key("rca"), &a)).To(Succeed())
		Expect(a.Status.FiringRules).To(Equal(int32(1)))
		Expect(a.Status.ProjectID).To(Equal(e.projectID))
		Expect(readyCondition(&a).Reason).To(Equal(v1.ReasonEvaluated))
		st := ruleStatus(e, "hourly-budget")
		Expect(st.State).To(Equal(v1.RuleFiring))
		Expect(st.Value).To(Equal("$13.0000"))
		Expect(st.LastNotified).NotTo(BeNil())

		By("not re-sending before the repeat interval")
		Consistently(func() []string { return slackServer.messages(path) }, 2*time.Second, 200*time.Millisecond).Should(HaveLen(1))

		By("raising the budget, which resolves the alert")
		a.Spec.Rules[0].Budget.MaxCostUSD = "50"
		Expect(k8sClient.Update(ctx, &a)).To(Succeed())
		Eventually(func() []string { return slackServer.messages(path) }, timeout, interval).Should(HaveLen(2))
		Expect(slackServer.messages(path)[1]).To(HavePrefix("[RESOLVED] rca-agent: hourly-budget"))
		Eventually(func() v1.RuleState { return ruleStatus(e, "hourly-budget").State }, timeout, interval).
			Should(Equal(v1.RuleOK))
	})

	It("detects spikes, cost-per-trace growth and unpriced models", func() {
		e := newEnv(true)
		path := "/services/" + e.ns
		e.createWebhookSecret(path)
		// Previous hour: 4 traces at $0.50. Last hour: 4 traces at $1.50, plus one unpriced model.
		for i := range 4 {
			e.seed("rca-agent", "production", 70*time.Minute+time.Duration(i)*time.Minute,
				fake.Generation{Model: "haiku", Tokens: 100, CostUSD: cost(0.5)})
			e.seed("rca-agent", "production", 10*time.Minute+time.Duration(i)*time.Minute,
				fake.Generation{Model: "sonnet", Tokens: 100, CostUSD: cost(1.5)})
		}
		e.seed("rca-agent", "production", 5*time.Minute,
			fake.Generation{Model: "us.anthropic.claude-sonnet-4-6", Tokens: 9000})

		hour := metav1.Duration{Duration: time.Hour}
		up := int32(100)
		Expect(k8sClient.Create(ctx, costAlert(e, "rca",
			v1.CostAlertRule{Name: "spike", Change: &v1.ChangeRule{Window: hour, IncreasePercent: &up, MinCostUSD: "1"}},
			v1.CostAlertRule{Name: "per-run", CostPerTrace: &v1.CostPerTraceRule{Window: hour, IncreasePercent: 100, MinTraces: 3}},
			v1.CostAlertRule{Name: "unpriced", UnpricedUsage: &v1.UnpricedUsageRule{Window: hour}},
			budgetRule("1000"),
		))).To(Succeed())

		Eventually(func() []string { return slackServer.messages(path) }, timeout, interval).Should(HaveLen(3))
		joined := strings.Join(slackServer.messages(path), "\n")
		Expect(joined).To(ContainSubstring("rca-agent: spike"))
		Expect(joined).To(ContainSubstring("+200%"))
		Expect(joined).To(ContainSubstring("rca-agent: per-run"))
		Expect(joined).To(ContainSubstring("rca-agent: unpriced"))
		Expect(joined).To(ContainSubstring("us.anthropic.claude-sonnet-4-6 (9000 tokens)"))
		Expect(joined).NotTo(ContainSubstring("hourly-budget"))

		var a v1.LangfuseCostAlert
		Expect(k8sClient.Get(ctx, e.key("rca"), &a)).To(Succeed())
		Expect(a.Status.FiringRules).To(Equal(int32(3)))
		Expect(ruleStatus(e, "per-run").Value).To(Equal("$1.2000"))
		Expect(ruleStatus(e, "per-run").Baseline).To(Equal("$0.5000"))
	})

	It("keeps the alert pending when Slack fails, and waits for its Secret", func() {
		e := newEnv(true)
		e.seed("rca-agent", "production", 5*time.Minute, fake.Generation{Model: "sonnet", Tokens: 1, CostUSD: cost(20)})
		Expect(k8sClient.Create(ctx, costAlert(e, "rca", budgetRule("10")))).To(Succeed())
		waitReason(e, &v1.LangfuseCostAlert{}, "rca", v1.ReasonSecretNotFound)

		e.createWebhookSecret("/services/" + e.ns + "/fail")
		var a v1.LangfuseCostAlert
		Expect(k8sClient.Get(ctx, e.key("rca"), &a)).To(Succeed())
		a.Spec.Notification.Slack.Mentions = nil // spec change triggers an evaluation
		Expect(k8sClient.Update(ctx, &a)).To(Succeed())
		waitReason(e, &v1.LangfuseCostAlert{}, "rca", v1.ReasonNotificationFailed)
		st := ruleStatus(e, "hourly-budget")
		Expect(st.FiringSince).NotTo(BeNil())
		Expect(st.LastNotified).To(BeNil())

		By("retrying soon once Slack works again, without waiting for the interval")
		var secret corev1.Secret
		Expect(k8sClient.Get(ctx, e.key("slack"), &secret)).To(Succeed())
		secret.Data["webhook_url"] = []byte(slackServer.URL + "/services/" + e.ns)
		Expect(k8sClient.Update(ctx, &secret)).To(Succeed())
		Eventually(func() []string { return slackServer.messages("/services/" + e.ns) }, timeout, interval).Should(HaveLen(1))
		waitReason(e, &v1.LangfuseCostAlert{}, "rca", v1.ReasonEvaluated)
		Expect(ruleStatus(e, "hourly-budget").LastNotified).NotTo(BeNil())
	})

	It("does nothing while suspended", func() {
		e := newEnv(true)
		path := "/services/" + e.ns
		e.createWebhookSecret(path)
		e.seed("rca-agent", "production", 5*time.Minute, fake.Generation{Model: "sonnet", Tokens: 1, CostUSD: cost(20)})
		a := costAlert(e, "rca", budgetRule("10"))
		a.Spec.Suspend = true
		Expect(k8sClient.Create(ctx, a)).To(Succeed())
		waitReason(e, &v1.LangfuseCostAlert{}, "rca", v1.ReasonSuspended)
		Expect(slackServer.messages(path)).To(BeEmpty())
	})

	It("reports specs the schema cannot check", func() {
		e := newEnv(true)
		e.createWebhookSecret("/services/" + e.ns)
		up := int32(10)
		Expect(k8sClient.Create(ctx, costAlert(e, "rca", v1.CostAlertRule{Name: "c", Change: &v1.ChangeRule{
			Window: metav1.Duration{Duration: time.Hour}, CompareOffset: dur(time.Minute), IncreasePercent: &up}}))).To(Succeed())
		waitReason(e, &v1.LangfuseCostAlert{}, "rca", v1.ReasonInvalidSpec)
	})

	DescribeTable("rejects invalid rules at admission",
		func(rule v1.CostAlertRule, message string) {
			e := newEnv(false)
			err := k8sClient.Create(ctx, costAlert(e, "bad", rule))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
			Expect(err.Error()).To(ContainSubstring(message))
		},
		Entry("two rule types", v1.CostAlertRule{Name: "x",
			Budget:        &v1.BudgetRule{Window: metav1.Duration{Duration: time.Hour}, MaxCostUSD: "1"},
			UnpricedUsage: &v1.UnpricedUsageRule{Window: metav1.Duration{Duration: time.Hour}}},
			"exactly one of budget"),
		Entry("no rule type", v1.CostAlertRule{Name: "x"}, "exactly one of budget"),
		Entry("change without a direction", v1.CostAlertRule{Name: "x",
			Change: &v1.ChangeRule{Window: metav1.Duration{Duration: time.Hour}}}, "increasePercent, decreasePercent"),
		Entry("bad budget", v1.CostAlertRule{Name: "x",
			Budget: &v1.BudgetRule{Window: metav1.Duration{Duration: time.Hour}, MaxCostUSD: "ten"}}, "maxCostUSD"),
		Entry("bad rule name", v1.CostAlertRule{Name: "Hourly Budget",
			Budget: &v1.BudgetRule{Window: metav1.Duration{Duration: time.Hour}, MaxCostUSD: "1"}}, "name"),
	)
})
