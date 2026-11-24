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

package modeldef

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	v1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
)

func spec(tiers ...v1.PricingTier) *v1.LangfuseModelSpec {
	return &v1.LangfuseModelSpec{
		ModelName:    "claude-sonnet-4-6-bedrock-geo",
		MatchPattern: `(?i)^(us|eu|apac)\.anthropic\.claude-sonnet-4-6$`,
		PricingTiers: tiers,
	}
}

func TestDesiredExpandsAliasesExactly(t *testing.T) {
	req, err := Desired(spec(v1.PricingTier{
		Name: "Standard", IsDefault: true,
		PricePerMillion: map[string]v1.Price{"input": "3.30", "output": "16.50", "cacheRead": "0.33", "cacheWrite": "4.125"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"input": "0.0000033", "input_tokens": "0.0000033",
		"output": "0.0000165", "output_tokens": "0.0000165",
		"input_cache_read": "0.00000033", "cache_read_input_tokens": "0.00000033",
		"input_cache_creation": "0.000004125", "cache_creation_input_tokens": "0.000004125",
	}
	got := req.PricingTiers[0].Prices
	if len(got) != len(want) {
		t.Fatalf("got %d prices, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if string(got[k]) != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if req.Unit != "TOKENS" || req.TokenizerID != nil || len(req.PricingTiers[0].Conditions) != 0 {
		t.Errorf("unexpected defaults: unit=%q tokenizer=%v conditions=%v", req.Unit, req.TokenizerID, req.PricingTiers[0].Conditions)
	}
}

func TestPricePrecedence(t *testing.T) {
	prices, err := ExpandPrices(&v1.PricingTier{
		Name: "Standard",
		// literal key beats alias within the same map
		PricePerMillion: map[string]v1.Price{"input": "3", "output": "15", "output_tokens": "16", "reasoning_tokens": "15"},
		// pricePerUnit beats pricePerMillion
		PricePerUnit: map[string]v1.Price{"input": "0.000005"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"input": "0.000005", "input_tokens": "0.000005",
		"output": "0.000015", "output_tokens": "0.000016", "reasoning_tokens": "0.000015",
	} {
		if got := formatRat(prices[k]); got != v {
			t.Errorf("%s = %s, want %s", k, got, v)
		}
	}
}

func TestDesiredRejectsInvalidSpecs(t *testing.T) {
	cases := map[string]*v1.LangfuseModelSpec{
		"bad price": spec(v1.PricingTier{Name: "Standard", IsDefault: true,
			PricePerUnit: map[string]v1.Price{"input": "abc"}}),
		"no prices": spec(v1.PricingTier{Name: "Standard", IsDefault: true}),
		"tiers price different usage types": spec(
			v1.PricingTier{Name: "Standard", IsDefault: true,
				PricePerMillion: map[string]v1.Price{"input": "3", "output": "15"}},
			v1.PricingTier{Name: "Large", Priority: 1,
				Conditions:      []v1.PricingTierCondition{{UsageDetailPattern: "input", Operator: "gt", Value: 200000}},
				PricePerMillion: map[string]v1.Price{"input": "6"}}),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Desired(s)
			var inv *InvalidSpecError
			if !errors.As(err, &inv) {
				t.Fatalf("want InvalidSpecError, got %v", err)
			}
		})
	}
}

func TestDiff(t *testing.T) {
	cfg := &apiextensionsv1.JSON{Raw: []byte(`{"tokensPerMessage": 3}`)}
	s := spec(
		v1.PricingTier{Name: "Standard", IsDefault: true,
			PricePerMillion: map[string]v1.Price{"input": "0.30", "output": "15"}},
		v1.PricingTier{Name: "Large Context", Priority: 1,
			Conditions:      []v1.PricingTierCondition{{UsageDetailPattern: "input", Operator: "gt", Value: 200000}},
			PricePerMillion: map[string]v1.Price{"input": "0.60", "output": "22.5"}},
	)
	s.TokenizerID = "claude"
	s.TokenizerConfig = cfg
	want, err := Desired(s)
	if err != nil {
		t.Fatal(err)
	}

	// What Langfuse returns: prices as JS numbers, tiers possibly unordered, IDs set.
	claude := "claude"
	have := func() *langfuse.Model {
		return &langfuse.Model{
			ID: "m1", ModelName: s.ModelName, MatchPattern: s.MatchPattern, Unit: "TOKENS",
			TokenizerID: &claude, TokenizerConfig: json.RawMessage(`{ "tokensPerMessage":3 }`),
			PricingTiers: []langfuse.PricingTier{
				{ID: "t2", Name: "Large Context", Priority: 1,
					Conditions: []langfuse.Condition{{UsageDetailPattern: "input", Operator: "gt", Value: "200000"}},
					Prices: map[string]json.Number{"input": "6e-7", "input_tokens": "6e-7",
						"output": "0.0000225", "output_tokens": "0.0000225"}},
				{ID: "t1", Name: "Standard", IsDefault: true, Conditions: []langfuse.Condition{},
					Prices: map[string]json.Number{"input": "3e-7", "input_tokens": "3e-7",
						"output": "0.000015", "output_tokens": "0.000015"}},
			},
		}
	}
	if d := Diff(want, have()); d != "" {
		t.Fatalf("expected no diff, got %q", d)
	}

	mutations := map[string]func(m *langfuse.Model){
		"price":         func(m *langfuse.Model) { m.PricingTiers[1].Prices["output"] = "0.000016" },
		"missing price": func(m *langfuse.Model) { delete(m.PricingTiers[1].Prices, "output") },
		"pattern":       func(m *langfuse.Model) { m.MatchPattern = "x" },
		"tokenizer":     func(m *langfuse.Model) { m.TokenizerID = nil },
		"config":        func(m *langfuse.Model) { m.TokenizerConfig = json.RawMessage(`{"tokensPerMessage":4}`) },
		"condition":     func(m *langfuse.Model) { m.PricingTiers[0].Conditions[0].Value = "100000" },
		"tier count":    func(m *langfuse.Model) { m.PricingTiers = m.PricingTiers[1:] },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := have()
			mutate(m)
			if d := Diff(want, m); d == "" {
				t.Fatal("expected a diff")
			}
		})
	}
}

func TestJSONEqualTreatsEmptyAsNull(t *testing.T) {
	for _, pair := range [][2]string{{"", "null"}, {"{}", ""}, {`{"a":1}`, `{ "a": 1 }`}} {
		if !jsonEqual(json.RawMessage(pair[0]), json.RawMessage(pair[1])) {
			t.Errorf("%q and %q should be equal", pair[0], pair[1])
		}
	}
	if jsonEqual(json.RawMessage(`{"a":1}`), nil) {
		t.Error("non-empty config must differ from null")
	}
}

func TestAliasesAreUnique(t *testing.T) {
	seen := map[string]string{}
	for alias, types := range Aliases {
		for _, u := range types {
			if prev, ok := seen[u]; ok {
				t.Errorf("usage type %q is produced by both %q and %q", u, prev, alias)
			}
			seen[u] = alias
			if strings.TrimSpace(u) != u || u == "" {
				t.Errorf("bad usage type %q", u)
			}
		}
	}
}
