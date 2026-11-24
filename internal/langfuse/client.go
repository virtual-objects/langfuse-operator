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

// Package langfuse is a minimal client for the Langfuse public API, limited to
// what the operator manages. Every call is authenticated with a project API key.
package langfuse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ClientName is sent in the X-From and User-Agent headers.
	ClientName     = "langfuse-operator"
	defaultTimeout = 30 * time.Second
	pageLimit      = 100
	maxErrorBody   = 4096
)

// Version is the operator version reported in User-Agent; set with -ldflags.
var Version = "dev"

// Client talks to one Langfuse project.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
	publicKey  string
	secretKey  string
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.httpClient = c }
}

// New returns a client for the Langfuse instance at host.
func New(host, publicKey, secretKey string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(host, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid Langfuse host %q", host)
	}
	if publicKey == "" || secretKey == "" {
		return nil, errors.New("public and secret key must not be empty")
	}
	c := &Client{
		baseURL:    u,
		httpClient: &http.Client{Timeout: defaultTimeout},
		publicKey:  publicKey,
		secretKey:  secretKey,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// APIError is a non-2xx response from Langfuse.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("langfuse %s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Message)
}

func statusOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// IsNotFound reports a 404 response.
func IsNotFound(err error) bool { return statusOf(err) == http.StatusNotFound }

// IsUnauthorized reports a 401 or 403 response, i.e. a bad or wrongly scoped key.
func IsUnauthorized(err error) bool {
	s := statusOf(err)
	return s == http.StatusUnauthorized || s == http.StatusForbidden
}

// IsRejected reports a request Langfuse refused as invalid (400 or 422).
// Retrying the same request will not help.
func IsRejected(err error) bool {
	s := statusOf(err)
	return s == http.StatusBadRequest || s == http.StatusUnprocessableEntity
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := c.baseURL.JoinPath(path)
	if query != nil {
		u.RawQuery = query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.SetBasicAuth(c.publicKey, c.secretKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-From", ClientName)
	req.Header.Set("User-Agent", ClientName+"/"+Version)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("langfuse %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return &APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Message: errorMessage(raw)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}

// errorMessage extracts Langfuse's {"message": ..., "error": ...} error body.
func errorMessage(raw []byte) string {
	var e struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		if len(e.Error) > 0 && e.Error[0] == '[' {
			return e.Message + ": " + string(e.Error)
		}
		return e.Message
	}
	return strings.TrimSpace(string(raw))
}

// GetProject returns the project the API key belongs to.
func (c *Client) GetProject(ctx context.Context) (*Project, error) {
	var resp struct {
		Data []Project `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/public/projects", nil, nil, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) != 1 {
		return nil, fmt.Errorf("expected exactly one project for a project-scoped key, got %d", len(resp.Data))
	}
	return &resp.Data[0], nil
}

// ListModels returns all models visible to the project, including built-in ones.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	var all []Model
	for page := 1; ; page++ {
		var resp struct {
			Data []Model `json:"data"`
			Meta struct {
				TotalPages int `json:"totalPages"`
			} `json:"meta"`
		}
		q := url.Values{"page": {fmt.Sprint(page)}, "limit": {fmt.Sprint(pageLimit)}}
		if err := c.do(ctx, http.MethodGet, "/api/public/models", q, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data...)
		if page >= resp.Meta.TotalPages || len(resp.Data) == 0 {
			return all, nil
		}
	}
}

// GetModel returns one model by ID.
func (c *Client) GetModel(ctx context.Context, id string) (*Model, error) {
	var m Model
	if err := c.do(ctx, http.MethodGet, "/api/public/models/"+url.PathEscape(id), nil, nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// CreateModel creates a project-scoped model.
func (c *Client) CreateModel(ctx context.Context, in *CreateModelRequest) (*Model, error) {
	var m Model
	if err := c.do(ctx, http.MethodPost, "/api/public/models", nil, in, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteModel deletes a project-scoped model. Built-in models cannot be deleted.
func (c *Client) DeleteModel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/public/models/"+url.PathEscape(id), nil, nil, nil)
}
