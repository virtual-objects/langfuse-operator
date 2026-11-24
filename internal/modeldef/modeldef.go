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

// Package modeldef turns a LangfuseModel spec into a Langfuse API request and
// decides whether an existing Langfuse model matches it.
package modeldef

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	langfusev1alpha1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
)

// Aliases maps a price alias to every usage-type name Langfuse and its SDKs
// report for it. Add new aliases here; keys that are not aliases are passed
// to Langfuse as literal usage types.
var Aliases = map[string][]string{
	"input":      {"input", "input_tokens"},
	"output":     {"output", "output_tokens"},
	"cacheRead":  {"input_cache_read", "cache_read_input_tokens"},
	"cacheWrite": {"input_cache_creation", "cache_creation_input_tokens"},
	"total":      {"total"},
}

var million = big.NewRat(1_000_000, 1)

// InvalidSpecError is a spec problem the user must fix; retrying will not help.
type InvalidSpecError struct{ msg string }

func (e *InvalidSpecError) Error() string { return e.msg }

func invalid(format string, a ...any) error {
	return &InvalidSpecError{msg: fmt.Sprintf(format, a...)}
}

func parseRat(s string) (*big.Rat, bool) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok || r.Sign() < 0 {
		return nil, false
	}
	return r, true
}

// formatRat prints r as an exact decimal. Callers only pass values whose
// denominators are powers of ten, which always have a finite expansion.
func formatRat(r *big.Rat) string {
	for prec := 0; prec <= 60; prec++ {
		s := r.FloatString(prec)
		if back, _ := new(big.Rat).SetString(s); back.Cmp(r) == 0 {
			return s
		}
	}
	return r.FloatString(30)
}

// sortedKeys returns alias keys first, then literal usage types, each sorted.
// Literal keys are applied last so they override alias expansion.
func sortedKeys(m map[string]langfusev1alpha1.Price) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		_, ai := Aliases[keys[i]]
		_, aj := Aliases[keys[j]]
		if ai != aj {
			return ai
		}
		return keys[i] < keys[j]
	})
	return keys
}

func applyPrices(out map[string]*big.Rat, in map[string]langfusev1alpha1.Price, divisor *big.Rat, field string) error {
	for _, k := range sortedKeys(in) {
		v, ok := parseRat(string(in[k]))
		if !ok {
			return invalid("%s[%s]: %q is not a non-negative decimal", field, k, in[k])
		}
		if divisor != nil {
			v = new(big.Rat).Quo(v, divisor)
		}
		targets, isAlias := Aliases[k]
		if !isAlias {
			targets = []string{k}
		}
		for _, t := range targets {
			out[t] = v
		}
	}
	return nil
}

// ExpandPrices returns the per-unit price for every usage type of a tier.
// Order of precedence (later wins): pricePerMillion aliases, pricePerMillion
// literals, pricePerUnit aliases, pricePerUnit literals.
func ExpandPrices(t *langfusev1alpha1.PricingTier) (map[string]*big.Rat, error) {
	out := map[string]*big.Rat{}
	if err := applyPrices(out, t.PricePerMillion, million, "pricePerMillion"); err != nil {
		return nil, err
	}
	if err := applyPrices(out, t.PricePerUnit, nil, "pricePerUnit"); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, invalid("pricing tier %q has no prices", t.Name)
	}
	return out, nil
}

// Desired builds the create request for a LangfuseModel spec.
func Desired(spec *langfusev1alpha1.LangfuseModelSpec) (*langfuse.CreateModelRequest, error) {
	unit := string(spec.Unit)
	if unit == "" {
		unit = "TOKENS"
	}
	req := &langfuse.CreateModelRequest{
		ModelName:    spec.ModelName,
		MatchPattern: spec.MatchPattern,
		Unit:         unit,
	}
	if spec.TokenizerID != "" {
		id := spec.TokenizerID
		req.TokenizerID = &id
	}
	if spec.TokenizerConfig != nil && len(spec.TokenizerConfig.Raw) > 0 {
		req.TokenizerConfig = json.RawMessage(spec.TokenizerConfig.Raw)
	}

	var defaultKeys []string
	for i := range spec.PricingTiers {
		t := &spec.PricingTiers[i]
		prices, err := ExpandPrices(t)
		if err != nil {
			return nil, err
		}
		tier := langfuse.PricingTier{
			Name:       t.Name,
			IsDefault:  t.IsDefault,
			Priority:   t.Priority,
			Conditions: []langfuse.Condition{},
			Prices:     make(map[string]json.Number, len(prices)),
		}
		for k, v := range prices {
			tier.Prices[k] = json.Number(formatRat(v))
		}
		for _, c := range t.Conditions {
			tier.Conditions = append(tier.Conditions, langfuse.Condition{
				UsageDetailPattern: c.UsageDetailPattern,
				Operator:           string(c.Operator),
				Value:              json.Number(fmt.Sprint(c.Value)),
				CaseSensitive:      c.CaseSensitive,
			})
		}
		if t.IsDefault {
			defaultKeys = keysOf(tier.Prices)
		}
		req.PricingTiers = append(req.PricingTiers, tier)
	}
	sort.Slice(req.PricingTiers, func(i, j int) bool { return req.PricingTiers[i].Priority < req.PricingTiers[j].Priority })

	// Langfuse requires every tier to price the same usage types.
	for _, t := range req.PricingTiers {
		if keys := keysOf(t.Prices); strings.Join(keys, ",") != strings.Join(defaultKeys, ",") {
			return nil, invalid("pricing tier %q must price the same usage types as the default tier: want [%s], got [%s]",
				t.Name, strings.Join(defaultKeys, ", "), strings.Join(keys, ", "))
		}
	}
	return req, nil
}

func keysOf(m map[string]json.Number) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
