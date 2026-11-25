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

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// TraceFilter narrows metrics to some traces of the project. Empty fields match all.
type TraceFilter struct {
	TraceName   string
	Environment string
	Tags        []string
}

// TraceStats is the number of traces and their total cost in a time range.
type TraceStats struct {
	Traces  int64
	CostUSD float64
}

// ModelUsage is token use and cost per model reported by generations.
type ModelUsage struct {
	Model   string
	Tokens  int64
	CostUSD float64
	// Priced is false when Langfuse computed no cost at all for these generations.
	Priced bool
}

// MetricsQuery is the query object of GET /api/public/metrics.
type MetricsQuery struct {
	View          string            `json:"view"`
	Dimensions    []MetricDimension `json:"dimensions"`
	Metrics       []Metric          `json:"metrics"`
	Filters       []MetricFilter    `json:"filters"`
	FromTimestamp string            `json:"fromTimestamp"`
	ToTimestamp   string            `json:"toTimestamp"`
}

// MetricDimension groups results by a field.
type MetricDimension struct {
	Field string `json:"field"`
}

// Metric is an aggregated measure. Results are keyed "<aggregation>_<measure>".
type Metric struct {
	Measure     string `json:"measure"`
	Aggregation string `json:"aggregation"`
}

// MetricFilter restricts the rows a query aggregates.
type MetricFilter struct {
	Column   string `json:"column"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
	Type     string `json:"type"`
}

// Metrics runs a metrics query and returns the raw rows.
func (c *Client) Metrics(ctx context.Context, q *MetricsQuery) ([]map[string]any, error) {
	b, err := json.Marshal(q)
	if err != nil {
		return nil, fmt.Errorf("encode metrics query: %w", err)
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/public/metrics", url.Values{"query": {string(b)}}, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

const (
	filterString = "string"
	aggSum       = "sum"
)

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func traceFilters(f TraceFilter, nameColumn string) []MetricFilter {
	out := []MetricFilter{}
	if f.TraceName != "" {
		out = append(out, MetricFilter{Column: nameColumn, Operator: "=", Value: f.TraceName, Type: filterString})
	}
	if f.Environment != "" {
		out = append(out, MetricFilter{Column: "environment", Operator: "=", Value: f.Environment, Type: filterString})
	}
	if len(f.Tags) > 0 {
		out = append(out, MetricFilter{Column: "tags", Operator: "all of", Value: f.Tags, Type: "arrayOptions"})
	}
	return out
}

// TraceStats returns trace count and total cost for traces in [from, to).
func (c *Client) TraceStats(ctx context.Context, f TraceFilter, from, to time.Time) (*TraceStats, error) {
	rows, err := c.Metrics(ctx, &MetricsQuery{
		View:       "traces",
		Dimensions: []MetricDimension{},
		Metrics: []Metric{
			{Measure: "count", Aggregation: "count"},
			{Measure: "totalCost", Aggregation: aggSum},
		},
		Filters:       traceFilters(f, "name"),
		FromTimestamp: timestamp(from),
		ToTimestamp:   timestamp(to),
	})
	if err != nil {
		return nil, err
	}
	st := &TraceStats{}
	for _, r := range rows {
		n, _ := Number(r["count_count"])
		cost, _ := Number(r["sum_totalCost"])
		st.Traces += int64(n)
		st.CostUSD += cost
	}
	return st, nil
}

// ModelUsage returns tokens and cost per model for generations in [from, to).
func (c *Client) ModelUsage(ctx context.Context, f TraceFilter, from, to time.Time) ([]ModelUsage, error) {
	filters := append(traceFilters(f, "traceName"),
		MetricFilter{Column: "type", Operator: "=", Value: "GENERATION", Type: filterString})
	rows, err := c.Metrics(ctx, &MetricsQuery{
		View:       "observations",
		Dimensions: []MetricDimension{{Field: "providedModelName"}},
		Metrics: []Metric{
			{Measure: "totalTokens", Aggregation: aggSum},
			{Measure: "totalCost", Aggregation: aggSum},
		},
		Filters:       filters,
		FromTimestamp: timestamp(from),
		ToTimestamp:   timestamp(to),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ModelUsage, 0, len(rows))
	for _, r := range rows {
		model, _ := r["providedModelName"].(string)
		tokens, _ := Number(r["sum_totalTokens"])
		cost, priced := Number(r["sum_totalCost"])
		out = append(out, ModelUsage{Model: model, Tokens: int64(tokens), CostUSD: cost, Priced: priced && cost > 0})
	}
	return out, nil
}

// Number reads a metrics value. Langfuse returns numbers, numeric strings
// (for 64-bit integers) or null. ok is false for null or unparsable values.
func Number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
