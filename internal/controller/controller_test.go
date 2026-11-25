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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
)

const (
	timeout  = 15 * time.Second
	interval = 100 * time.Millisecond
)

// env is one isolated namespace with its own Langfuse project and connection.
type env struct {
	ns, projectID string
}

func newEnv(connect bool) env {
	e := env{ns: "t-" + rand.String(6), projectID: "proj-" + rand.String(6)}
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e.ns}})).To(Succeed())
	lfServer.AddProject(e.projectID, "Project "+e.projectID, "pk-"+e.projectID, "sk-"+e.projectID)
	if connect {
		e.createSecret("lf-keys", "pk-"+e.projectID, "sk-"+e.projectID)
		e.createConnection("lf-keys")
		e.waitReady(&v1.LangfuseConnection{}, "lf")
	}
	return e
}

func (e env) key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: e.ns, Name: name}
}

func (e env) createSecret(name, pk, sk string) {
	Expect(k8sClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: name},
		StringData: map[string]string{"public_key": pk, "secret_key": sk},
	})).To(Succeed())
}

// createConnection creates the connection "lf" that sonnet() models reference.
func (e env) createConnection(secret string) {
	Expect(k8sClient.Create(ctx, &v1.LangfuseConnection{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "lf"},
		Spec: v1.LangfuseConnectionSpec{
			Host:            lfServer.URL,
			APIKeySecretRef: v1.SecretKeyRef{Name: secret},
		},
	})).To(Succeed())
}

func readyCondition(obj client.Object) *metav1.Condition {
	switch o := obj.(type) {
	case *v1.LangfuseConnection:
		return meta.FindStatusCondition(o.Status.Conditions, v1.ConditionReady)
	case *v1.LangfuseModel:
		return meta.FindStatusCondition(o.Status.Conditions, v1.ConditionReady)
	case *v1.LangfuseCostAlert:
		return meta.FindStatusCondition(o.Status.Conditions, v1.ConditionReady)
	}
	return nil
}

// waitReason waits until obj's Ready condition has the given reason.
func waitReason(e env, obj client.Object, name, reason string) {
	GinkgoHelper()
	waitReasonWithin(e, obj, name, reason, timeout)
}

func waitReasonWithin(e env, obj client.Object, name, reason string, within time.Duration) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, e.key(name), obj)).To(Succeed())
		c := readyCondition(obj)
		g.Expect(c).NotTo(BeNil())
		g.Expect(c.Reason).To(Equal(reason), c.Message)
	}, within, interval).Should(Succeed())
}

func (e env) waitReady(obj client.Object, name string) {
	GinkgoHelper()
	waitReason(e, obj, name, v1.ReasonSynced)
}

func sonnet(e env, name, modelName string) *v1.LangfuseModel {
	return &v1.LangfuseModel{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: name},
		Spec: v1.LangfuseModelSpec{
			ConnectionRef: v1.LocalObjectReference{Name: "lf"},
			ModelName:     modelName,
			MatchPattern:  `(?i)^(us|eu|apac)\.anthropic\.claude-sonnet-4-6$`,
			TokenizerID:   "claude",
			PricingTiers: []v1.PricingTier{{
				Name: "Standard", IsDefault: true,
				PricePerMillion: map[string]v1.Price{"input": "3.30", "output": "16.50", "cacheRead": "0.33", "cacheWrite": "4.125"},
			}},
		},
	}
}

// projectModels returns the project's own models in the fake Langfuse.
func (e env) projectModels() []langfuse.Model { return lfServer.Models(e.projectID) }

func (e env) modelByName(name string) *langfuse.Model {
	for _, m := range e.projectModels() {
		if m.ModelName == name {
			return &m
		}
	}
	return nil
}

func priceOf(m *langfuse.Model, usage string) string {
	if m == nil || len(m.PricingTiers) == 0 {
		return ""
	}
	return string(m.PricingTiers[0].Prices[usage])
}

var _ = Describe("LangfuseConnection", func() {
	It("records the project of a valid key", func() {
		e := newEnv(true)
		var conn v1.LangfuseConnection
		Expect(k8sClient.Get(ctx, e.key("lf"), &conn)).To(Succeed())
		Expect(conn.Status.ProjectID).To(Equal(e.projectID))
		Expect(conn.Status.ProjectName).To(Equal("Project " + e.projectID))
		Expect(conn.Status.ObservedGeneration).To(Equal(conn.Generation))
	})

	It("reports a missing Secret and recovers once it exists", func() {
		e := newEnv(false)
		e.createConnection("late")
		waitReason(e, &v1.LangfuseConnection{}, "lf", v1.ReasonSecretNotFound)
		e.createSecret("late", "pk-"+e.projectID, "sk-"+e.projectID)
		// Secrets are read with "get" only (no watch), so recovery happens on the next retry.
		waitReasonWithin(e, &v1.LangfuseConnection{}, "lf", v1.ReasonSynced, retryShort+timeout)
	})

	It("reports a rejected key", func() {
		e := newEnv(false)
		e.createSecret("bad", "pk-"+e.projectID, "wrong")
		e.createConnection("bad")
		waitReason(e, &v1.LangfuseConnection{}, "lf", v1.ReasonUnauthorized)
	})

	It("rejects a non-http host", func() {
		e := newEnv(false)
		err := k8sClient.Create(ctx, &v1.LangfuseConnection{
			ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "lf"},
			Spec:       v1.LangfuseConnectionSpec{Host: "ftp://x", APIKeySecretRef: v1.SecretKeyRef{Name: "s"}},
		})
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
	})
})

var _ = Describe("LangfuseModel", func() {
	It("creates the model with expanded per-unit prices and stays idempotent", func() {
		e := newEnv(true)
		Expect(k8sClient.Create(ctx, sonnet(e, "geo", "claude-sonnet-4-6-bedrock-geo"))).To(Succeed())
		var m v1.LangfuseModel
		e.waitReady(&m, "geo")

		got := e.modelByName("claude-sonnet-4-6-bedrock-geo")
		Expect(got).NotTo(BeNil())
		Expect(m.Status.ModelID).To(Equal(got.ID))
		Expect(m.Status.ProjectID).To(Equal(e.projectID))
		Expect(m.Finalizers).To(ContainElement(Finalizer))
		Expect(*got.TokenizerID).To(Equal("claude"))
		Expect(got.PricingTiers[0].Prices).To(HaveLen(8))
		Expect(priceOf(got, "input_tokens")).To(Equal("3.3e-06"))
		Expect(priceOf(got, "cache_read_input_tokens")).To(Equal("3.3e-07"))
		Expect(priceOf(got, "input_cache_creation")).To(Equal("4.125e-06"))

		By("not recreating the model on later resyncs")
		id := got.ID
		Consistently(func() string {
			if g := e.modelByName("claude-sonnet-4-6-bedrock-geo"); g != nil {
				return g.ID
			}
			return ""
		}, 3*testResync, 200*time.Millisecond).Should(Equal(id))
	})

	It("applies a price change by recreating the model", func() {
		e := newEnv(true)
		Expect(k8sClient.Create(ctx, sonnet(e, "geo", "geo"))).To(Succeed())
		var m v1.LangfuseModel
		e.waitReady(&m, "geo")
		oldID := m.Status.ModelID

		m.Spec.PricingTiers[0].PricePerMillion["output"] = "17"
		Expect(k8sClient.Update(ctx, &m)).To(Succeed())
		Eventually(func() string { return priceOf(e.modelByName("geo"), "output_tokens") }, timeout, interval).
			Should(Equal("1.7e-05"))
		Expect(e.projectModels()).To(HaveLen(1))
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, e.key("geo"), &m)).To(Succeed())
			g.Expect(m.Status.ModelID).NotTo(Equal(oldID))
			g.Expect(m.Status.ObservedGeneration).To(Equal(m.Generation))
		}, timeout, interval).Should(Succeed())
	})

	It("corrects drift made outside Kubernetes", func() {
		e := newEnv(true)
		Expect(k8sClient.Create(ctx, sonnet(e, "geo", "geo"))).To(Succeed())
		e.waitReady(&v1.LangfuseModel{}, "geo")
		lfServer.RemoveModel(e.modelByName("geo").ID)
		Eventually(func() *langfuse.Model { return e.modelByName("geo") }, timeout, interval).ShouldNot(BeNil())
	})

	It("adopts a hand-made model with the same name and fixes its prices", func() {
		e := newEnv(true)
		handMade := lfServer.AddModel(e.projectID, langfuse.Model{
			ModelName: "geo", MatchPattern: "(?i)^geo$", Unit: "TOKENS",
			PricingTiers: []langfuse.PricingTier{{Name: "Standard", IsDefault: true,
				Prices: map[string]json.Number{"input": "0.000003"}}},
		})
		Expect(k8sClient.Create(ctx, sonnet(e, "geo", "geo"))).To(Succeed())
		e.waitReady(&v1.LangfuseModel{}, "geo")
		Expect(e.projectModels()).To(HaveLen(1))
		Expect(e.modelByName("geo").ID).NotTo(Equal(handMade))
		Expect(priceOf(e.modelByName("geo"), "output")).To(Equal("1.65e-05"))
	})

	It("overrides a built-in model without touching it", func() {
		e := newEnv(true)
		name := "builtin-" + e.projectID
		builtin := lfServer.AddModel("", langfuse.Model{ModelName: name, MatchPattern: "x", Unit: "TOKENS",
			PricingTiers: []langfuse.PricingTier{{Name: "Standard", IsDefault: true,
				Prices: map[string]json.Number{"input": "0.000003"}}}})
		Expect(k8sClient.Create(ctx, sonnet(e, "override", name))).To(Succeed())
		var m v1.LangfuseModel
		e.waitReady(&m, "override")
		Expect(m.Status.ModelID).NotTo(Equal(builtin))
		Expect(e.projectModels()).To(HaveLen(1))
	})

	It("removes the old model when modelName changes", func() {
		e := newEnv(true)
		Expect(k8sClient.Create(ctx, sonnet(e, "geo", "old-name"))).To(Succeed())
		var m v1.LangfuseModel
		e.waitReady(&m, "geo")
		m.Spec.ModelName = "new-name"
		Expect(k8sClient.Update(ctx, &m)).To(Succeed())
		Eventually(func() []string {
			models := e.projectModels()
			names := make([]string, 0, len(models))
			for _, lm := range models {
				names = append(names, lm.ModelName)
			}
			return names
		}, timeout, interval).Should(Equal([]string{"new-name"}))
	})

	It("flags a second resource managing the same model as a conflict", func() {
		e := newEnv(true)
		Expect(k8sClient.Create(ctx, sonnet(e, "first", "geo"))).To(Succeed())
		e.waitReady(&v1.LangfuseModel{}, "first")
		second := sonnet(e, "second", "geo")
		second.Spec.PricingTiers[0].PricePerMillion["output"] = "99"
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		waitReason(e, &v1.LangfuseModel{}, "second", v1.ReasonConflict)
		Expect(priceOf(e.modelByName("geo"), "output")).To(Equal("1.65e-05"))
	})

	It("waits for its connection", func() {
		e := newEnv(false)
		Expect(k8sClient.Create(ctx, sonnet(e, "geo", "geo"))).To(Succeed())
		waitReason(e, &v1.LangfuseModel{}, "geo", v1.ReasonConnectionNotReady)
		e.createSecret("lf-keys", "pk-"+e.projectID, "sk-"+e.projectID)
		e.createConnection("lf-keys")
		e.waitReady(&v1.LangfuseModel{}, "geo")
	})

	It("reports specs that Langfuse would reject", func() {
		e := newEnv(true)
		m := sonnet(e, "mismatch", "mismatch")
		m.Spec.PricingTiers = append(m.Spec.PricingTiers, v1.PricingTier{
			Name: "Large", Priority: 1,
			Conditions:      []v1.PricingTierCondition{{UsageDetailPattern: "input", Operator: "gt", Value: 200000}},
			PricePerMillion: map[string]v1.Price{"input": "6"},
		})
		Expect(k8sClient.Create(ctx, m)).To(Succeed())
		waitReason(e, &v1.LangfuseModel{}, "mismatch", v1.ReasonInvalidSpec)
		Expect(e.projectModels()).To(BeEmpty())
	})

	DescribeTable("rejects invalid pricing tiers at admission",
		func(mutate func(*v1.LangfuseModel), message string) {
			e := newEnv(false)
			m := sonnet(e, "bad", "bad")
			mutate(m)
			err := k8sClient.Create(ctx, m)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
			Expect(err.Error()).To(ContainSubstring(message))
		},
		Entry("two default tiers", func(m *v1.LangfuseModel) {
			m.Spec.PricingTiers = append(m.Spec.PricingTiers, v1.PricingTier{Name: "B", IsDefault: true, Priority: 1,
				PricePerUnit: map[string]v1.Price{"input": "1"}})
		}, "exactly one pricing tier"),
		Entry("default tier with conditions", func(m *v1.LangfuseModel) {
			m.Spec.PricingTiers[0].Conditions = []v1.PricingTierCondition{{UsageDetailPattern: "input", Operator: "gt", Value: 1}}
		}, "default pricing tier must have priority 0"),
		Entry("non-default tier without conditions", func(m *v1.LangfuseModel) {
			m.Spec.PricingTiers = append(m.Spec.PricingTiers, v1.PricingTier{Name: "B", Priority: 1,
				PricePerUnit: map[string]v1.Price{"input": "1"}})
		}, "needs at least one condition"),
		Entry("duplicate priorities", func(m *v1.LangfuseModel) {
			m.Spec.PricingTiers = append(m.Spec.PricingTiers, v1.PricingTier{Name: "B",
				Conditions:   []v1.PricingTierCondition{{UsageDetailPattern: "input", Operator: "gt", Value: 1}},
				PricePerUnit: map[string]v1.Price{"input": "1"}})
		}, "priorities must be unique"),
		Entry("negative-looking price", func(m *v1.LangfuseModel) {
			m.Spec.PricingTiers[0].PricePerMillion["input"] = "-1"
		}, "pricePerMillion"),
		Entry("tier without prices", func(m *v1.LangfuseModel) {
			m.Spec.PricingTiers[0].PricePerMillion = nil
		}, "at least one price"),
	)

	It("deletes the Langfuse model with the resource", func() {
		e := newEnv(true)
		Expect(k8sClient.Create(ctx, sonnet(e, "geo", "geo"))).To(Succeed())
		var m v1.LangfuseModel
		e.waitReady(&m, "geo")
		Expect(k8sClient.Delete(ctx, &m)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, e.key("geo"), &m)) }, timeout, interval).Should(BeTrue())
		Expect(e.projectModels()).To(BeEmpty())
	})

	It("keeps the Langfuse model with deletionPolicy Orphan", func() {
		e := newEnv(true)
		m := sonnet(e, "geo", "geo")
		m.Spec.DeletionPolicy = v1.DeletionPolicyOrphan
		Expect(k8sClient.Create(ctx, m)).To(Succeed())
		e.waitReady(&v1.LangfuseModel{}, "geo")
		Expect(k8sClient.Delete(ctx, m)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, e.key("geo"), m)) }, timeout, interval).Should(BeTrue())
		Expect(e.projectModels()).To(HaveLen(1))
	})

	It("does not block deletion when the connection is gone", func() {
		e := newEnv(true)
		Expect(k8sClient.Create(ctx, sonnet(e, "geo", "geo"))).To(Succeed())
		var m v1.LangfuseModel
		e.waitReady(&m, "geo")
		Expect(k8sClient.Delete(ctx, &v1.LangfuseConnection{ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "lf"}})).To(Succeed())
		Expect(k8sClient.Delete(ctx, &m)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, e.key("geo"), &m)) }, timeout, interval).Should(BeTrue())
	})

	It("sends the X-From header on every call", func() {
		_ = lfServer.Requests()
		e := newEnv(true)
		Expect(k8sClient.Create(ctx, sonnet(e, "geo", "geo"))).To(Succeed())
		e.waitReady(&v1.LangfuseModel{}, "geo")
		reqs := lfServer.Requests()
		Expect(reqs).NotTo(BeEmpty())
		for _, r := range reqs {
			Expect(r.XFrom).To(Equal(langfuse.ClientName), fmt.Sprintf("%s %s", r.Method, r.Path))
		}
	})
})

var _ = Describe("Samples", func() {
	It("are accepted by the API server", func() {
		e := newEnv(false)
		files, err := filepath.Glob(filepath.Join("..", "..", "config", "samples", "langfuse_*.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(files).NotTo(BeEmpty())
		for _, f := range files {
			raw, err := os.ReadFile(f)
			Expect(err).NotTo(HaveOccurred())
			dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
			for {
				var u unstructured.Unstructured
				if err := dec.Decode(&u.Object); err != nil {
					break
				}
				if len(u.Object) == 0 {
					continue
				}
				u.SetNamespace(e.ns)
				Expect(k8sClient.Create(ctx, &u, client.DryRunAll)).To(Succeed(), "%s: %s", f, u.GetName())
			}
		}
	})
})
