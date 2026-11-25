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

package langfuse_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse/fake"
)

// newClient returns a client authenticated as project p1's public key "pk".
func newClient(t *testing.T, srv *fake.Server, sk string) *langfuse.Client {
	t.Helper()
	c, err := langfuse.New(srv.URL+"/", "pk", sk)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tier(prices map[string]json.Number) []langfuse.PricingTier {
	return []langfuse.PricingTier{{Name: "Standard", IsDefault: true, Conditions: []langfuse.Condition{}, Prices: prices}}
}

func TestClientLifecycle(t *testing.T) {
	srv := fake.NewServer()
	defer srv.Close()
	srv.AddProject("p1", "proj", "pk", "sk")
	ctx := context.Background()
	c := newClient(t, srv, "sk")

	p, err := c.GetProject(ctx)
	if err != nil || p.ID != "p1" || p.Name != "proj" {
		t.Fatalf("GetProject = %+v, %v", p, err)
	}

	in := &langfuse.CreateModelRequest{ModelName: "m", MatchPattern: "(?i)^m$", Unit: "TOKENS",
		PricingTiers: tier(map[string]json.Number{"input": "0.0000003"})}
	m, err := c.CreateModel(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.PricingTiers[0].Prices["input"]; got != "3e-07" {
		t.Errorf("fake should return JS-style numbers, got %s", got)
	}

	_, err = c.CreateModel(ctx, in)
	if !langfuse.IsRejected(err) {
		t.Fatalf("duplicate name should be rejected, got %v", err)
	}

	if _, err := c.GetModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteModel(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetModel(ctx, m.ID); !langfuse.IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}

	for _, r := range srv.Requests() {
		if r.XFrom != langfuse.ClientName {
			t.Errorf("%s %s sent X-From %q", r.Method, r.Path, r.XFrom)
		}
	}
}

func TestListModelsPaginatesAndIncludesBuiltIns(t *testing.T) {
	srv := fake.NewServer()
	defer srv.Close()
	srv.AddProject("p1", "proj", "pk", "sk")
	srv.AddProject("p2", "other", "pk2", "sk2")
	for i := range 230 {
		srv.AddModel("", langfuse.Model{ModelName: fmt.Sprintf("builtin-%03d", i), PricingTiers: tier(nil)})
	}
	srv.AddModel("p1", langfuse.Model{ModelName: "mine", PricingTiers: tier(nil)})
	srv.AddModel("p2", langfuse.Model{ModelName: "theirs", PricingTiers: tier(nil)})

	models, err := newClient(t, srv, "sk").ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 231 {
		t.Fatalf("got %d models, want 231", len(models))
	}
	own := 0
	for _, m := range models {
		if m.ModelName == "theirs" {
			t.Fatal("saw another project's model")
		}
		if !m.IsLangfuseManaged {
			own++
		}
	}
	if own != 1 {
		t.Fatalf("got %d project models, want 1", own)
	}
}

func TestErrorClassification(t *testing.T) {
	srv := fake.NewServer()
	defer srv.Close()
	srv.AddProject("p1", "proj", "pk", "sk")
	builtin := srv.AddModel("", langfuse.Model{ModelName: "gpt", PricingTiers: tier(nil)})
	ctx := context.Background()

	if _, err := newClient(t, srv, "wrong").GetProject(ctx); !langfuse.IsUnauthorized(err) {
		t.Fatalf("want unauthorized, got %v", err)
	}
	if err := newClient(t, srv, "sk").DeleteModel(ctx, builtin); !langfuse.IsNotFound(err) {
		t.Fatalf("deleting a built-in model must 404, got %v", err)
	}
	if _, err := langfuse.New("ftp://x", "a", "b"); err == nil {
		t.Fatal("want error for non-http host")
	}
	if _, err := langfuse.New("http://x", "", "b"); err == nil {
		t.Fatal("want error for empty key")
	}
}

func TestTraceStatsAndModelUsage(t *testing.T) {
	srv := fake.NewServer()
	defer srv.Close()
	srv.AddProject("p1", "proj", "pk", "sk")
	now := time.Now().UTC()
	c1, c2 := 0.25, 0.5
	srv.AddTrace("p1", fake.Trace{Name: "rca", Environment: "prod", Tags: []string{"team:sre", "x"}, Timestamp: now.Add(-time.Minute),
		Generations: []fake.Generation{{Model: "sonnet", Tokens: 100, CostUSD: &c1}, {Model: "mystery", Tokens: 40}}})
	srv.AddTrace("p1", fake.Trace{Name: "rca", Environment: "staging", Timestamp: now.Add(-time.Minute),
		Generations: []fake.Generation{{Model: "sonnet", Tokens: 10, CostUSD: &c2}}})
	srv.AddTrace("p1", fake.Trace{Name: "chat", Environment: "prod", Timestamp: now.Add(-2 * time.Hour),
		Generations: []fake.Generation{{Model: "sonnet", Tokens: 10, CostUSD: &c2}}})
	c := newClient(t, srv, "sk")
	ctx := context.Background()

	st, err := c.TraceStats(ctx, langfuse.TraceFilter{TraceName: "rca", Environment: "prod", Tags: []string{"team:sre"}},
		now.Add(-time.Hour), now)
	if err != nil || st.Traces != 1 || st.CostUSD != 0.25 {
		t.Fatalf("TraceStats = %+v, %v", st, err)
	}
	st, err = c.TraceStats(ctx, langfuse.TraceFilter{TraceName: "nobody"}, now.Add(-time.Hour), now)
	if err != nil || st.Traces != 0 || st.CostUSD != 0 {
		t.Fatalf("empty TraceStats = %+v, %v", st, err)
	}
	usage, err := c.ModelUsage(ctx, langfuse.TraceFilter{TraceName: "rca", Environment: "prod"}, now.Add(-time.Hour), now)
	if err != nil || len(usage) != 2 {
		t.Fatalf("ModelUsage = %+v, %v", usage, err)
	}
	for _, u := range usage {
		if (u.Model == "sonnet") != u.Priced {
			t.Errorf("priced flag wrong for %+v", u)
		}
	}
}

func TestNumber(t *testing.T) {
	for in, want := range map[any]float64{float64(1.5): 1.5, "42": 42, json.Number("0.25"): 0.25} {
		if got, ok := langfuse.Number(in); !ok || got != want {
			t.Errorf("Number(%v) = %v, %v", in, got, ok)
		}
	}
	if _, ok := langfuse.Number(nil); ok {
		t.Error("null must not be ok")
	}
}
