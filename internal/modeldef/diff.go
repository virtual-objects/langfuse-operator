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
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"sort"

	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
)

// Diff returns "" when the Langfuse model matches the desired request, or a
// short human-readable description of the first difference found.
func Diff(want *langfuse.CreateModelRequest, have *langfuse.Model) string {
	switch {
	case want.ModelName != have.ModelName:
		return fmt.Sprintf("modelName %q != %q", have.ModelName, want.ModelName)
	case want.MatchPattern != have.MatchPattern:
		return "matchPattern changed"
	case want.Unit != have.Unit:
		return fmt.Sprintf("unit %q != %q", have.Unit, want.Unit)
	case deref(want.TokenizerID) != deref(have.TokenizerID):
		return "tokenizerId changed"
	case !jsonEqual(want.TokenizerConfig, have.TokenizerConfig):
		return "tokenizerConfig changed"
	case len(want.PricingTiers) != len(have.PricingTiers):
		return fmt.Sprintf("%d pricing tiers != %d", len(have.PricingTiers), len(want.PricingTiers))
	}
	haveTiers := append([]langfuse.PricingTier(nil), have.PricingTiers...)
	sort.Slice(haveTiers, func(i, j int) bool { return haveTiers[i].Priority < haveTiers[j].Priority })
	for i := range want.PricingTiers {
		if d := diffTier(&want.PricingTiers[i], &haveTiers[i]); d != "" {
			return fmt.Sprintf("pricing tier %q: %s", want.PricingTiers[i].Name, d)
		}
	}
	return ""
}

func diffTier(want, have *langfuse.PricingTier) string {
	switch {
	case want.Name != have.Name:
		return fmt.Sprintf("name %q != %q", have.Name, want.Name)
	case want.IsDefault != have.IsDefault:
		return "isDefault changed"
	case want.Priority != have.Priority:
		return "priority changed"
	case len(want.Conditions) != len(have.Conditions):
		return "conditions changed"
	case len(want.Prices) != len(have.Prices):
		return "priced usage types changed"
	}
	for i := range want.Conditions {
		w, h := want.Conditions[i], have.Conditions[i]
		if w.UsageDetailPattern != h.UsageDetailPattern || w.Operator != h.Operator ||
			w.CaseSensitive != h.CaseSensitive || !numEqual(w.Value, h.Value) {
			return "conditions changed"
		}
	}
	for k, v := range want.Prices {
		hv, ok := have.Prices[k]
		if !ok {
			return fmt.Sprintf("price %q missing", k)
		}
		if !numEqual(v, hv) {
			return fmt.Sprintf("price %q %s != %s", k, hv, v)
		}
	}
	return ""
}

// numEqual compares two JSON numbers exactly. Langfuse returns prices as
// JavaScript numbers (e.g. 3e-7), which are the shortest float that round-trips;
// compare in float64 when exact decimal comparison fails for that reason.
func numEqual(a, b json.Number) bool {
	ra, okA := new(big.Rat).SetString(string(a))
	rb, okB := new(big.Rat).SetString(string(b))
	if !okA || !okB {
		return a == b
	}
	if ra.Cmp(rb) == 0 {
		return true
	}
	fa, _ := ra.Float64()
	fb, _ := rb.Float64()
	return fa == fb
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// jsonEqual treats missing, null and {} as equal and otherwise compares JSON semantically.
func jsonEqual(a, b json.RawMessage) bool {
	norm := func(r json.RawMessage) any {
		r = bytes.TrimSpace(r)
		if len(r) == 0 || string(r) == "null" {
			return nil
		}
		var v any
		if json.Unmarshal(r, &v) != nil {
			return string(r)
		}
		if m, ok := v.(map[string]any); ok && len(m) == 0 {
			return nil
		}
		return v
	}
	return reflect.DeepEqual(norm(a), norm(b))
}
