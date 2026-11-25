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

package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSlackRejectsBadURLsWithoutLeakingThem(t *testing.T) {
	for _, u := range []string{"", "ftp://hooks.slack.com/x", "not a url", "https://"} {
		_, err := NewSlack(u, "")
		if err == nil {
			t.Errorf("%q: expected error", u)
			continue
		}
		if u != "" && strings.Contains(err.Error(), u) {
			t.Errorf("error leaks the URL: %v", err)
		}
	}
}

func TestSendFiringAndResolved(t *testing.T) {
	var got []map[string]any
	var xFrom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xFrom = r.Header.Get("X-From")
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got = append(got, m)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	s, err := NewSlack(srv.URL+"/services/T/B/secret", "langfuse-operator/test")
	if err != nil {
		t.Fatal(err)
	}
	m := &Message{
		Title: "rca-agent: hourly-budget", Summary: "cost $12 is over the $10 budget",
		Fields:  []Field{{"Project", "rca <prod>"}, {"Value", "$12.0000"}},
		LinkURL: "https://langfuse.example.com/project/p1/traces", LinkText: "Open in Langfuse",
		Mentions: []string{"<!subteam^S1>"}, Source: "sre/rca-agent-cost",
	}
	if err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	m.Resolved = true
	if err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if xFrom != "langfuse-operator" {
		t.Errorf("X-From = %q", xFrom)
	}
	firing, resolved := got[0]["text"].(string), got[1]["text"].(string)
	if !strings.HasPrefix(firing, "<!subteam^S1> [FIRING] rca-agent: hourly-budget") {
		t.Errorf("firing text = %q", firing)
	}
	if strings.Contains(resolved, "subteam") || !strings.HasPrefix(resolved, "[RESOLVED]") {
		t.Errorf("resolved text = %q", resolved)
	}
	raw, _ := json.Marshal(got[0])
	if !strings.Contains(string(raw), `rca \u0026lt;prod\u0026gt;`) {
		t.Errorf("user text must be escaped: %s", raw)
	}
}

func TestSendReportsSlackErrorsWithoutURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("invalid_token"))
	}))
	defer srv.Close()
	s, _ := NewSlack(srv.URL+"/services/T/B/very-secret", "")
	err := s.Send(context.Background(), &Message{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("got %v", err)
	}
	s.WebhookURL = "http://127.0.0.1:1/services/very-secret"
	err = s.Send(context.Background(), &Message{Title: "t"})
	if err == nil || strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("connection error must not leak the URL: %v", err)
	}
}
