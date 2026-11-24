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

package langfuse

import "encoding/json"

// Project is a Langfuse project.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Condition selects a pricing tier from usage details.
type Condition struct {
	UsageDetailPattern string      `json:"usageDetailPattern"`
	Operator           string      `json:"operator"`
	Value              json.Number `json:"value"`
	CaseSensitive      bool        `json:"caseSensitive"`
}

// PricingTier is a named set of prices. Prices are kept as json.Number so
// decimal values survive the round trip without float rounding.
type PricingTier struct {
	ID         string                 `json:"id,omitempty"`
	Name       string                 `json:"name"`
	IsDefault  bool                   `json:"isDefault"`
	Priority   int32                  `json:"priority"`
	Conditions []Condition            `json:"conditions"`
	Prices     map[string]json.Number `json:"prices"`
}

// Model is a model definition as returned by the API.
type Model struct {
	ID                string          `json:"id"`
	ModelName         string          `json:"modelName"`
	MatchPattern      string          `json:"matchPattern"`
	Unit              string          `json:"unit,omitempty"`
	TokenizerID       *string         `json:"tokenizerId"`
	TokenizerConfig   json.RawMessage `json:"tokenizerConfig,omitempty"`
	IsLangfuseManaged bool            `json:"isLangfuseManaged"`
	PricingTiers      []PricingTier   `json:"pricingTiers"`
}

// CreateModelRequest is the body of POST /api/public/models.
type CreateModelRequest struct {
	ModelName       string          `json:"modelName"`
	MatchPattern    string          `json:"matchPattern"`
	Unit            string          `json:"unit"`
	TokenizerID     *string         `json:"tokenizerId,omitempty"`
	TokenizerConfig json.RawMessage `json:"tokenizerConfig,omitempty"`
	PricingTiers    []PricingTier   `json:"pricingTiers"`
}
