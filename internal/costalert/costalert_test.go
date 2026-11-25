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

package costalert

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
)

var end = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// fakeSource returns stats by window start time.
type fakeSource struct {
	stats map[time.Time]langfuse.TraceStats
	usage []langfuse.ModelUsage
	err   error
	calls []time.Time
}

func (f *fakeSource) TraceStats(_ context.Context, _ langfuse.TraceFilter, from, _ time.Time) (*langfuse.TraceStats, error) {
	f.calls = append(f.calls, from)
	if f.err != nil {
		return nil, f.err
	}
	st := f.stats[from]
	return &st, nil
}

func (f *fakeSource) ModelUsage(context.Context, langfuse.TraceFilter, time.Time, time.Time) ([]langfuse.ModelUsage, error) {
	return f.usage, f.err
}

func d(x time.Duration) metav1.Duration            { return metav1.Duration{Duration: x} }
func dp(x time.Duration) *metav1.Duration          { v := d(x); return &v }
func i32(x int32) *int32                           { return &x }
func stats(n int64, c float64) langfuse.TraceStats { return langfuse.TraceStats{Traces: n, CostUSD: c} }

func TestBudget(t *testing.T) {
	r := &v1.CostAlertRule{Name: "b", Budget: &v1.BudgetRule{Window: d(time.Hour), MaxCostUSD: "10"}}
	for _, tc := range []struct {
		cost float64
		want v1.RuleState
	}{{9.99, v1.RuleOK}, {10, v1.RuleOK}, {10.01, v1.RuleFiring}, {0, v1.RuleOK}} {
		src := &fakeSource{stats: map[time.Time]langfuse.TraceStats{end.Add(-time.Hour): stats(5, tc.cost)}}
		if got := Evaluate(context.Background(), src, langfuse.TraceFilter{}, r, end); got.State != tc.want {
			t.Errorf("cost %v: got %s (%s), want %s", tc.cost, got.State, got.Message, tc.want)
		}
	}
}

func TestChange(t *testing.T) {
	cur, prev, week := end.Add(-time.Hour), end.Add(-2*time.Hour), end.Add(-169*time.Hour)
	cases := []struct {
		name  string
		rule  v1.ChangeRule
		stats map[time.Time]langfuse.TraceStats
		want  v1.RuleState
		msg   string
	}{
		{"spike", v1.ChangeRule{IncreasePercent: i32(50)},
			map[time.Time]langfuse.TraceStats{cur: stats(10, 16), prev: stats(10, 10)}, v1.RuleFiring, "+60%"},
		{"small rise", v1.ChangeRule{IncreasePercent: i32(50)},
			map[time.Time]langfuse.TraceStats{cur: stats(10, 14), prev: stats(10, 10)}, v1.RuleOK, "+40%"},
		{"drop", v1.ChangeRule{DecreasePercent: i32(90)},
			map[time.Time]langfuse.TraceStats{cur: stats(0, 0), prev: stats(10, 10)}, v1.RuleFiring, "-100%"},
		{"drop ignored by increase-only rule", v1.ChangeRule{IncreasePercent: i32(50)},
			map[time.Time]langfuse.TraceStats{cur: stats(1, 1), prev: stats(10, 10)}, v1.RuleOK, "-90%"},
		{"noise below minimum", v1.ChangeRule{IncreasePercent: i32(50), MinCostUSD: "5"},
			map[time.Time]langfuse.TraceStats{cur: stats(3, 4), prev: stats(3, 1)}, v1.RuleOK, "minimum"},
		{"new spend", v1.ChangeRule{IncreasePercent: i32(50), MinCostUSD: "5"},
			map[time.Time]langfuse.TraceStats{cur: stats(3, 8)}, v1.RuleFiring, "new spend"},
		{"new spend without minimum stays quiet", v1.ChangeRule{IncreasePercent: i32(50)},
			map[time.Time]langfuse.TraceStats{cur: stats(3, 8)}, v1.RuleOK, "no cost"},
		{"no data", v1.ChangeRule{IncreasePercent: i32(50)}, nil, v1.RuleNoData, "no traces"},
		{"week over week", v1.ChangeRule{IncreasePercent: i32(50), CompareOffset: dp(168 * time.Hour)},
			map[time.Time]langfuse.TraceStats{cur: stats(10, 20), week: stats(10, 10), prev: stats(10, 20)},
			v1.RuleFiring, "168h"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.rule.Window = d(time.Hour)
			r := &v1.CostAlertRule{Name: "c", Change: &tc.rule}
			got := Evaluate(context.Background(), &fakeSource{stats: tc.stats}, langfuse.TraceFilter{}, r, end)
			if got.State != tc.want || !strings.Contains(got.Message, tc.msg) {
				t.Fatalf("got %s %q, want %s containing %q", got.State, got.Message, tc.want, tc.msg)
			}
		})
	}
}

func TestCostPerTrace(t *testing.T) {
	cur, prev := end.Add(-time.Hour), end.Add(-2*time.Hour)
	r := &v1.CostAlertRule{Name: "p", CostPerTrace: &v1.CostPerTraceRule{Window: d(time.Hour), IncreasePercent: 100, MinTraces: 10}}
	// Same traffic, cost per trace doubled: e.g. a bigger model.
	got := Evaluate(context.Background(), &fakeSource{stats: map[time.Time]langfuse.TraceStats{
		cur: stats(20, 4), prev: stats(20, 2)}}, langfuse.TraceFilter{}, r, end)
	if got.State != v1.RuleFiring || got.Value != "$0.2000" || got.Baseline != "$0.1000" {
		t.Fatalf("got %+v", got)
	}
	// Twice the traffic at the same cost per trace is not a cost-per-trace problem.
	got = Evaluate(context.Background(), &fakeSource{stats: map[time.Time]langfuse.TraceStats{
		cur: stats(40, 4), prev: stats(20, 2)}}, langfuse.TraceFilter{}, r, end)
	if got.State != v1.RuleOK {
		t.Fatalf("got %+v", got)
	}
	got = Evaluate(context.Background(), &fakeSource{stats: map[time.Time]langfuse.TraceStats{
		cur: stats(5, 4), prev: stats(20, 2)}}, langfuse.TraceFilter{}, r, end)
	if got.State != v1.RuleNoData {
		t.Fatalf("too few traces should be NoData, got %+v", got)
	}
}

func TestUnpriced(t *testing.T) {
	r := &v1.CostAlertRule{Name: "u", UnpricedUsage: &v1.UnpricedUsageRule{Window: d(time.Hour), MinTokens: 100}}
	src := &fakeSource{usage: []langfuse.ModelUsage{
		{Model: "gpt-4o", Tokens: 5000, CostUSD: 0.01, Priced: true},
		{Model: "anthropic.claude-sonnet-4-6", Tokens: 9000},
		{Model: "", Tokens: 200},
		{Model: "tiny", Tokens: 50},
	}}
	got := Evaluate(context.Background(), src, langfuse.TraceFilter{}, r, end)
	if got.State != v1.RuleFiring || got.Value != "9200 tokens" ||
		!strings.Contains(got.Message, "anthropic.claude-sonnet-4-6 (9000 tokens), (no model name) (200 tokens)") ||
		strings.Contains(got.Message, "tiny") || strings.Contains(got.Message, "gpt-4o") {
		t.Fatalf("got %+v", got)
	}
}

func TestEvaluateError(t *testing.T) {
	r := &v1.CostAlertRule{Name: "b", Budget: &v1.BudgetRule{Window: d(time.Hour), MaxCostUSD: "1"}}
	got := Evaluate(context.Background(), &fakeSource{err: errors.New("boom")}, langfuse.TraceFilter{}, r, end)
	if got.State != v1.RuleError || got.Message != "boom" {
		t.Fatalf("got %+v", got)
	}
}

func TestValidate(t *testing.T) {
	ok := v1.LangfuseCostAlertSpec{Rules: []v1.CostAlertRule{{Name: "c",
		Change: &v1.ChangeRule{Window: d(time.Hour), CompareOffset: dp(24 * time.Hour), IncreasePercent: i32(10)}}}}
	if err := Validate(&ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]v1.LangfuseCostAlertSpec{
		"offset shorter than window": {Rules: []v1.CostAlertRule{{Name: "c",
			Change: &v1.ChangeRule{Window: d(time.Hour), CompareOffset: dp(time.Minute), IncreasePercent: i32(10)}}}},
		"window too long": {Rules: []v1.CostAlertRule{{Name: "b",
			Budget: &v1.BudgetRule{Window: d(800 * time.Hour), MaxCostUSD: "1"}}}},
		"interval too short": {Interval: dp(time.Second), Rules: ok.Rules},
	}
	for name, s := range bad {
		if err := Validate(&s); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestApply(t *testing.T) {
	t0 := end
	repeat := time.Hour
	var sent []Action
	ok := func(a Action) error { sent = append(sent, a); return nil }
	fail := func(Action) error { return errors.New("slack down") }
	firing := Result{State: v1.RuleFiring}

	// OK -> Firing notifies once.
	s, err := Apply(nil, "r", firing, t0, repeat, true, ok)
	if err != nil || len(sent) != 1 || sent[0] != Firing || s.FiringSince == nil || !s.LastNotified.Time.Equal(t0) {
		t.Fatalf("first firing: %+v %v %v", s, sent, err)
	}
	// Still firing within the repeat interval: no notification.
	s, _ = Apply(&s, "r", firing, t0.Add(30*time.Minute), repeat, true, ok)
	if len(sent) != 1 {
		t.Fatalf("should not repeat yet: %v", sent)
	}
	// Errors and missing data do not resolve an active alert.
	s, _ = Apply(&s, "r", Result{State: v1.RuleError}, t0.Add(40*time.Minute), repeat, true, ok)
	s, _ = Apply(&s, "r", Result{State: v1.RuleNoData}, t0.Add(45*time.Minute), repeat, true, ok)
	if s.FiringSince == nil || len(sent) != 1 {
		t.Fatalf("error/no data must keep the alert: %+v %v", s, sent)
	}
	// After the repeat interval it is sent again.
	s, _ = Apply(&s, "r", firing, t0.Add(61*time.Minute), repeat, true, ok)
	if len(sent) != 2 || !s.FiringSince.Time.Equal(t0) {
		t.Fatalf("repeat: %+v %v", s, sent)
	}
	// Resolved notification failure keeps the alert so it is retried.
	s2, err := Apply(&s, "r", Result{State: v1.RuleOK}, t0.Add(70*time.Minute), repeat, true, fail)
	if err == nil || s2.FiringSince == nil {
		t.Fatalf("failed resolve must keep alert: %+v %v", s2, err)
	}
	s, _ = Apply(&s2, "r", Result{State: v1.RuleOK}, t0.Add(75*time.Minute), repeat, true, ok)
	if len(sent) != 3 || sent[2] != Resolved || s.FiringSince != nil || s.LastNotified != nil {
		t.Fatalf("resolve: %+v %v", s, sent)
	}
	// sendResolved=false resolves silently.
	s, _ = Apply(nil, "r", firing, t0, repeat, false, ok)
	s, _ = Apply(&s, "r", Result{State: v1.RuleOK}, t0.Add(time.Minute), repeat, false, ok)
	if len(sent) != 4 || s.FiringSince != nil {
		t.Fatalf("silent resolve: %+v %v", s, sent)
	}
	// A failed first notification is retried on the next evaluation.
	s, err = Apply(nil, "r", firing, t0, repeat, true, fail)
	if err == nil || s.LastNotified != nil || s.FiringSince == nil {
		t.Fatalf("failed notify: %+v %v", s, err)
	}
	s, _ = Apply(&s, "r", firing, t0.Add(time.Minute), repeat, true, ok)
	if len(sent) != 5 || s.LastNotified == nil {
		t.Fatalf("retry: %+v %v", s, sent)
	}
}
