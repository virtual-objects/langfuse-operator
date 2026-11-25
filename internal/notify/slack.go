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

// Package notify delivers alert notifications. Slack is the first channel; new
// channels implement Sender.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Message is a channel-independent alert notification.
type Message struct {
	Resolved   bool
	Title      string // e.g. "rca-agent: hourly-budget"
	Summary    string // result message
	Fields     []Field
	LinkURL    string
	LinkText   string
	Mentions   []string
	Source     string // namespace/name of the LangfuseCostAlert
	OccurredAt time.Time
}

// Field is a labelled value shown in the notification.
type Field struct {
	Label, Value string
}

// Sender delivers a Message.
type Sender interface {
	Send(ctx context.Context, m *Message) error
}

// Slack posts to a Slack incoming webhook.
type Slack struct {
	WebhookURL string
	HTTPClient *http.Client
	// UserAgent identifies the operator version.
	UserAgent string
}

// NewSlack validates the webhook URL and returns a sender.
func NewSlack(webhookURL, userAgent string) (*Slack, error) {
	u, err := url.Parse(strings.TrimSpace(webhookURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		// Never echo the URL: it is a credential.
		return nil, fmt.Errorf("the Slack webhook URL in the Secret is not a valid http(s) URL")
	}
	return &Slack{WebhookURL: u.String(), HTTPClient: &http.Client{Timeout: 10 * time.Second}, UserAgent: userAgent}, nil
}

const mrkdwn = "mrkdwn"

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackBlock struct {
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Fields   []slackText `json:"fields,omitempty"`
	Elements []slackText `json:"elements,omitempty"`
}

type slackAttachment struct {
	Color  string       `json:"color"`
	Blocks []slackBlock `json:"blocks"`
}

type slackPayload struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

// escape makes text safe for Slack mrkdwn: user data cannot inject mentions or links.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// payload renders the Slack message.
func (s *Slack) payload(m *Message) slackPayload {
	icon, state, color := ":rotating_light:", "FIRING", "#D92D20"
	if m.Resolved {
		icon, state, color = ":white_check_mark:", "RESOLVED", "#12B76A"
	}
	headline := fmt.Sprintf("%s *[%s]* %s", icon, state, escape(m.Title))
	blocks := []slackBlock{
		{Type: "section", Text: &slackText{Type: mrkdwn, Text: headline + "\n" + escape(m.Summary)}},
	}
	if len(m.Fields) > 0 {
		fields := make([]slackText, 0, len(m.Fields))
		for _, f := range m.Fields {
			fields = append(fields, slackText{Type: mrkdwn, Text: fmt.Sprintf("*%s*\n%s", escape(f.Label), escape(f.Value))})
		}
		for len(fields) > 0 { // Slack allows at most 10 fields per section
			n := min(10, len(fields))
			blocks = append(blocks, slackBlock{Type: "section", Fields: fields[:n]})
			fields = fields[n:]
		}
	}
	var ctx []string
	if m.LinkURL != "" {
		ctx = append(ctx, fmt.Sprintf("<%s|%s>", m.LinkURL, escape(m.LinkText)))
	}
	ctx = append(ctx, "langfuse-operator · "+escape(m.Source))
	blocks = append(blocks, slackBlock{Type: "context", Elements: []slackText{{Type: mrkdwn, Text: strings.Join(ctx, " · ")}}})

	text := fmt.Sprintf("[%s] %s: %s", state, m.Title, m.Summary)
	if !m.Resolved && len(m.Mentions) > 0 {
		// Mentions only notify when they are in the top-level text.
		text = strings.Join(m.Mentions, " ") + " " + text
	}
	return slackPayload{Text: text, Attachments: []slackAttachment{{Color: color, Blocks: blocks}}}
}

// Send posts the message.
func (s *Slack) Send(ctx context.Context, m *Message) error {
	body, err := json.Marshal(s.payload(m))
	if err != nil {
		return fmt.Errorf("encode Slack message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build Slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-From", "langfuse-operator")
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		// The error from net/http contains the URL; report only the host.
		return fmt.Errorf("post to Slack webhook (%s): %w", req.URL.Host, unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func unwrapURLError(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}
