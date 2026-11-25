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

// Package costalert evaluates LangfuseCostAlert rules against Langfuse metrics
// and decides when to notify. It has no Kubernetes dependencies.
package costalert

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
)

// Limits for durations in the spec.
const (
	MaxWindow         = 31 * 24 * time.Hour
	MinInterval       = time.Minute
	MaxIngestionDelay = 24 * time.Hour
	DefaultInterval   = 15 * time.Minute
	DefaultDelay      = 5 * time.Minute
	DefaultRepeat     = 6 * time.Hour
)

// Source provides the metrics rules are evaluated on.
type Source interface {
	TraceStats(ctx context.Context, f langfuse.TraceFilter, from, to time.Time) (*langfuse.TraceStats, error)
	ModelUsage(ctx context.Context, f langfuse.TraceFilter, from, to time.Time) ([]langfuse.ModelUsage, error)
}

// Result is the outcome of evaluating one rule.
type Result struct {
	State    v1.RuleState
	Value    string
	Baseline string
	Message  string
	// Number is the measured value, exported as a Prometheus gauge.
	Number float64
}

func dur(d *metav1.Duration, def time.Duration) time.Duration {
	if d == nil {
		return def
	}
	return d.Duration
}

// Interval returns the evaluation interval of a spec.
func Interval(s *v1.LangfuseCostAlertSpec) time.Duration { return dur(s.Interval, DefaultInterval) }

// IngestionDelay returns the ingestion delay of a spec.
func IngestionDelay(s *v1.LangfuseCostAlertSpec) time.Duration {
	return dur(s.IngestionDelay, DefaultDelay)
}

// RepeatInterval returns how often a firing alert is re-sent.
func RepeatInterval(s *v1.LangfuseCostAlertSpec) time.Duration {
	return dur(s.Notification.RepeatInterval, DefaultRepeat)
}

// SendResolved reports whether recoveries are notified.
func SendResolved(s *v1.LangfuseCostAlertSpec) bool {
	return s.Notification.SendResolved == nil || *s.Notification.SendResolved
}

// Filter converts the agent selector into a metrics filter.
func Filter(a v1.AgentSelector) langfuse.TraceFilter {
	return langfuse.TraceFilter{TraceName: a.TraceName, Environment: a.Environment, Tags: a.Tags}
}

// windows returns the window and comparison offset of a rule.
func windows(r *v1.CostAlertRule) (window, offset time.Duration) {
	switch {
	case r.Budget != nil:
		return r.Budget.Window.Duration, 0
	case r.Change != nil:
		return r.Change.Window.Duration, dur(r.Change.CompareOffset, r.Change.Window.Duration)
	case r.CostPerTrace != nil:
		return r.CostPerTrace.Window.Duration, dur(r.CostPerTrace.CompareOffset, r.CostPerTrace.Window.Duration)
	case r.UnpricedUsage != nil:
		return r.UnpricedUsage.Window.Duration, 0
	}
	return 0, 0
}

// Validate checks what the CRD schema cannot express.
func Validate(s *v1.LangfuseCostAlertSpec) error {
	if d := Interval(s); d < MinInterval {
		return fmt.Errorf("interval %s is below the minimum of %s", d, MinInterval)
	}
	if d := IngestionDelay(s); d < 0 || d > MaxIngestionDelay {
		return fmt.Errorf("ingestionDelay %s must be between 0 and %s", d, MaxIngestionDelay)
	}
	if d := RepeatInterval(s); d < MinInterval {
		return fmt.Errorf("notification.repeatInterval %s is below the minimum of %s", d, MinInterval)
	}
	for i := range s.Rules {
		r := &s.Rules[i]
		w, off := windows(r)
		if w <= 0 || w > MaxWindow {
			return fmt.Errorf("rule %q: window must be between 1s and %s", r.Name, MaxWindow)
		}
		if off != 0 && (off < w || off > MaxWindow) {
			return fmt.Errorf("rule %q: compareOffset must be at least the window (%s) and at most %s", r.Name, w, MaxWindow)
		}
		if r.Budget != nil {
			if _, err := price(r.Budget.MaxCostUSD); err != nil {
				return fmt.Errorf("rule %q: maxCostUSD: %w", r.Name, err)
			}
		}
		if r.Change != nil && r.Change.MinCostUSD != "" {
			if _, err := price(r.Change.MinCostUSD); err != nil {
				return fmt.Errorf("rule %q: minCostUSD: %w", r.Name, err)
			}
		}
	}
	return nil
}

func price(p v1.Price) (float64, error) {
	if p == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(string(p), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%q is not a non-negative decimal", p)
	}
	return f, nil
}

// USD formats a cost for people.
func USD(v float64) string {
	switch {
	case v == 0:
		return "$0"
	case v < 0.01:
		return "$" + strconv.FormatFloat(v, 'f', 6, 64)
	case v < 100:
		return "$" + strconv.FormatFloat(v, 'f', 4, 64)
	default:
		return "$" + strconv.FormatFloat(v, 'f', 2, 64)
	}
}

func pct(cur, base float64) float64 { return (cur - base) / base * 100 }

// Evaluate runs one rule for windows that end at end.
func Evaluate(ctx context.Context, src Source, f langfuse.TraceFilter, r *v1.CostAlertRule, end time.Time) Result {
	w, off := windows(r)
	var res Result
	var err error
	switch {
	case r.Budget != nil:
		res, err = budget(ctx, src, f, r.Budget, end, w)
	case r.Change != nil:
		res, err = change(ctx, src, f, r.Change, end, w, off)
	case r.CostPerTrace != nil:
		res, err = costPerTrace(ctx, src, f, r.CostPerTrace, end, w, off)
	case r.UnpricedUsage != nil:
		res, err = unpriced(ctx, src, f, r.UnpricedUsage, end, w)
	default:
		err = fmt.Errorf("rule has no type")
	}
	if err != nil {
		return Result{State: v1.RuleError, Message: err.Error()}
	}
	return res
}

func budget(ctx context.Context, src Source, f langfuse.TraceFilter, b *v1.BudgetRule, end time.Time, w time.Duration) (Result, error) {
	limit, err := price(b.MaxCostUSD)
	if err != nil {
		return Result{}, err
	}
	st, err := src.TraceStats(ctx, f, end.Add(-w), end)
	if err != nil {
		return Result{}, err
	}
	res := Result{Value: USD(st.CostUSD), Baseline: USD(limit), Number: st.CostUSD, State: v1.RuleOK}
	if st.CostUSD > limit {
		res.State = v1.RuleFiring
		res.Message = fmt.Sprintf("cost %s in the last %s is over the %s budget (%d traces)",
			USD(st.CostUSD), w, USD(limit), st.Traces)
	} else {
		res.Message = fmt.Sprintf("cost %s in the last %s is within the %s budget", USD(st.CostUSD), w, USD(limit))
	}
	return res, nil
}

func change(ctx context.Context, src Source, f langfuse.TraceFilter, c *v1.ChangeRule, end time.Time,
	w, off time.Duration) (Result, error) {
	minCost, err := price(c.MinCostUSD)
	if err != nil {
		return Result{}, err
	}
	cur, err := src.TraceStats(ctx, f, end.Add(-w), end)
	if err != nil {
		return Result{}, err
	}
	base, err := src.TraceStats(ctx, f, end.Add(-off-w), end.Add(-off))
	if err != nil {
		return Result{}, err
	}
	res := Result{Value: USD(cur.CostUSD), Baseline: USD(base.CostUSD), Number: cur.CostUSD, State: v1.RuleOK}
	period := fmt.Sprintf("last %s vs. the %s before %s", w, w, off)
	if off == w {
		period = fmt.Sprintf("last %s vs. the previous %s", w, w)
	}
	switch {
	case cur.Traces == 0 && base.Traces == 0:
		res.State = v1.RuleNoData
		res.Message = "no traces in either window"
	case cur.CostUSD < minCost && base.CostUSD < minCost:
		res.Message = fmt.Sprintf("both windows are below the %s minimum", USD(minCost))
	case base.CostUSD == 0:
		if c.IncreasePercent != nil && minCost > 0 {
			res.State = v1.RuleFiring
			res.Message = fmt.Sprintf("new spend: %s (%s), none before", USD(cur.CostUSD), period)
		} else {
			res.Message = "no cost in the comparison window"
		}
	default:
		p := pct(cur.CostUSD, base.CostUSD)
		res.Message = fmt.Sprintf("cost %+.0f%%: %s vs. %s (%s)", p, USD(cur.CostUSD), USD(base.CostUSD), period)
		if c.IncreasePercent != nil && p >= float64(*c.IncreasePercent) {
			res.State = v1.RuleFiring
		}
		if c.DecreasePercent != nil && -p >= float64(*c.DecreasePercent) {
			res.State = v1.RuleFiring
		}
	}
	return res, nil
}

func costPerTrace(ctx context.Context, src Source, f langfuse.TraceFilter, c *v1.CostPerTraceRule, end time.Time,
	w, off time.Duration) (Result, error) {
	minTraces := c.MinTraces
	if minTraces <= 0 {
		minTraces = 10
	}
	cur, err := src.TraceStats(ctx, f, end.Add(-w), end)
	if err != nil {
		return Result{}, err
	}
	base, err := src.TraceStats(ctx, f, end.Add(-off-w), end.Add(-off))
	if err != nil {
		return Result{}, err
	}
	if cur.Traces < minTraces || base.Traces < minTraces {
		return Result{State: v1.RuleNoData, Message: fmt.Sprintf(
			"need at least %d traces in both windows (have %d and %d)", minTraces, cur.Traces, base.Traces)}, nil
	}
	curAvg := cur.CostUSD / float64(cur.Traces)
	baseAvg := base.CostUSD / float64(base.Traces)
	res := Result{Value: USD(curAvg), Baseline: USD(baseAvg), Number: curAvg, State: v1.RuleOK}
	if baseAvg == 0 {
		res.Message = "no cost per trace in the comparison window"
		return res, nil
	}
	p := pct(curAvg, baseAvg)
	res.Message = fmt.Sprintf("cost per trace %+.0f%%: %s vs. %s (%d vs. %d traces)",
		p, USD(curAvg), USD(baseAvg), cur.Traces, base.Traces)
	if p >= float64(c.IncreasePercent) {
		res.State = v1.RuleFiring
	}
	return res, nil
}

func unpriced(ctx context.Context, src Source, f langfuse.TraceFilter, u *v1.UnpricedUsageRule, end time.Time,
	w time.Duration) (Result, error) {
	minTokens := u.MinTokens
	if minTokens <= 0 {
		minTokens = 1
	}
	usage, err := src.ModelUsage(ctx, f, end.Add(-w), end)
	if err != nil {
		return Result{}, err
	}
	var bad []langfuse.ModelUsage
	var total int64
	for _, m := range usage {
		if !m.Priced && m.Tokens >= minTokens {
			bad = append(bad, m)
			total += m.Tokens
		}
	}
	res := Result{Value: strconv.FormatInt(total, 10) + " tokens", Number: float64(total), State: v1.RuleOK,
		Message: "all generations with tokens have a cost"}
	if len(bad) == 0 {
		return res, nil
	}
	sort.Slice(bad, func(i, j int) bool { return bad[i].Tokens > bad[j].Tokens })
	names := make([]string, 0, len(bad))
	for i, m := range bad {
		if i == 5 {
			names = append(names, fmt.Sprintf("and %d more", len(bad)-5))
			break
		}
		name := m.Model
		if name == "" {
			name = "(no model name)"
		}
		names = append(names, fmt.Sprintf("%s (%d tokens)", name, m.Tokens))
	}
	res.State = v1.RuleFiring
	res.Message = fmt.Sprintf("generations without a price in the last %s: %s. Add a LangfuseModel for them.",
		w, strings.Join(names, ", "))
	return res, nil
}
