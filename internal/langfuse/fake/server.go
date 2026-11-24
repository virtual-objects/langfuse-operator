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

// Package fake is an in-memory Langfuse public API for tests. It mirrors the
// behaviour of Langfuse v3 that the operator relies on: project-scoped keys,
// built-in (global) models, duplicate-name rejection, no update endpoint, and
// prices returned as JavaScript numbers.
package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
)

type project struct {
	id, name, secret string
}

// Server is a fake Langfuse. Create one with NewServer and Close it when done.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	keys     map[string]project // public key -> project
	models   map[string]*stored // model ID -> model
	nextID   int
	requests []Request
}

type stored struct {
	projectID string // empty for built-in models
	model     langfuse.Model
}

// Request is a recorded API call.
type Request struct {
	Method, Path, XFrom string
}

// NewServer starts a fake Langfuse.
func NewServer() *Server {
	s := &Server{keys: map[string]project{}, models: map[string]*stored{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// AddProject registers a project and its API key pair.
func (s *Server) AddProject(id, name, publicKey, secretKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[publicKey] = project{id: id, name: name, secret: secretKey}
}

// AddModel stores a model directly. An empty projectID adds a built-in model.
func (s *Server) AddModel(projectID string, m langfuse.Model) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insert(projectID, m)
}

func (s *Server) insert(projectID string, m langfuse.Model) string {
	s.nextID++
	m.ID = fmt.Sprintf("model-%d", s.nextID)
	m.IsLangfuseManaged = projectID == ""
	for i := range m.PricingTiers {
		m.PricingTiers[i].ID = fmt.Sprintf("%s-tier-%d", m.ID, i)
		m.PricingTiers[i].Prices = asJSNumbers(m.PricingTiers[i].Prices)
		if m.PricingTiers[i].Conditions == nil {
			m.PricingTiers[i].Conditions = []langfuse.Condition{}
		}
	}
	s.models[m.ID] = &stored{projectID: projectID, model: m}
	return m.ID
}

// RemoveModel deletes a model directly, simulating a change made outside the operator.
func (s *Server) RemoveModel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.models, id)
}

// Models returns the project's own (non built-in) models, sorted by name.
func (s *Server) Models(projectID string) []langfuse.Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []langfuse.Model
	for _, st := range s.models {
		if st.projectID == projectID {
			out = append(out, st.model)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModelName < out[j].ModelName })
	return out
}

// Requests returns the recorded calls and clears the log.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests
	s.requests = nil
	return r
}

// CountRequests counts recorded calls with the given method, without clearing them.
func (s *Server) CountRequests(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.Method == method {
			n++
		}
	}
	return n
}

// asJSNumbers stores prices the way Langfuse returns them: Prisma Decimal ->
// JavaScript number, so values such as 3e-7 come back in exponent form.
func asJSNumbers(in map[string]json.Number) map[string]json.Number {
	out := make(map[string]json.Number, len(in))
	for k, v := range in {
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			out[k] = v
			continue
		}
		out[k] = json.Number(strconv.FormatFloat(f, 'g', -1, 64))
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"message": msg, "error": http.StatusText(code)})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, XFrom: r.Header.Get("X-From")})

	pk, sk, ok := r.BasicAuth()
	p, known := s.keys[pk]
	if !ok || !known || p.secret != sk {
		fail(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	switch {
	case r.URL.Path == "/api/public/projects" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"data": []langfuse.Project{{ID: p.id, Name: p.name}}})
	case r.URL.Path == "/api/public/models" && r.Method == http.MethodGet:
		s.list(w, r, p)
	case r.URL.Path == "/api/public/models" && r.Method == http.MethodPost:
		s.create(w, r, p)
	case strings.HasPrefix(r.URL.Path, "/api/public/models/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/public/models/")
		st, found := s.models[id]
		visible := found && (st.projectID == "" || st.projectID == p.id)
		switch r.Method {
		case http.MethodGet:
			if !visible {
				fail(w, http.StatusNotFound, "No model with this id found.")
				return
			}
			writeJSON(w, http.StatusOK, st.model)
		case http.MethodDelete:
			if !found || st.projectID != p.id {
				fail(w, http.StatusNotFound, "No model with this id found. Note: You cannot delete built-in models, override them with a model with the same name.")
				return
			}
			delete(s.models, id)
			writeJSON(w, http.StatusOK, map[string]string{"message": "Model successfully deleted"})
		default:
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	default:
		fail(w, http.StatusNotFound, "Not found")
	}
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, p project) {
	var visible []langfuse.Model
	for _, st := range s.models {
		if st.projectID == "" || st.projectID == p.id {
			visible = append(visible, st.model)
		}
	}
	sort.Slice(visible, func(i, j int) bool {
		if visible[i].ModelName != visible[j].ModelName {
			return visible[i].ModelName < visible[j].ModelName
		}
		return visible[i].ID < visible[j].ID
	})
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	start := min((page-1)*limit, len(visible))
	end := min(start+limit, len(visible))
	totalPages := (len(visible) + limit - 1) / limit
	writeJSON(w, http.StatusOK, map[string]any{
		"data": visible[start:end],
		"meta": map[string]int{"page": page, "limit": limit, "totalItems": len(visible), "totalPages": totalPages},
	})
}

const invalidRequest = "Invalid request data"

var units = map[string]bool{"TOKENS": true, "CHARACTERS": true, "MILLISECONDS": true, "SECONDS": true, "REQUESTS": true, "IMAGES": true}

func (s *Server) create(w http.ResponseWriter, r *http.Request, p project) {
	var in langfuse.CreateModelRequest
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, invalidRequest)
		return
	}
	if msg := validate(&in); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	for _, st := range s.models {
		if st.projectID == p.id && st.model.ModelName == in.ModelName {
			fail(w, http.StatusBadRequest, fmt.Sprintf("Model name '%s' already exists in project", in.ModelName))
			return
		}
	}
	tiers := make([]langfuse.PricingTier, len(in.PricingTiers))
	copy(tiers, in.PricingTiers)
	id := s.insert(p.id, langfuse.Model{
		ModelName:       in.ModelName,
		MatchPattern:    in.MatchPattern,
		Unit:            in.Unit,
		TokenizerID:     in.TokenizerID,
		TokenizerConfig: in.TokenizerConfig,
		PricingTiers:    tiers,
	})
	writeJSON(w, http.StatusOK, s.models[id].model)
}

// validate mirrors the Langfuse request validation the operator can trigger.
func validate(in *langfuse.CreateModelRequest) string {
	switch {
	case in.ModelName == "" || in.MatchPattern == "":
		return invalidRequest
	case !units[in.Unit]:
		return invalidRequest
	case in.TokenizerID != nil && *in.TokenizerID != "openai" && *in.TokenizerID != "claude":
		return invalidRequest
	case len(in.PricingTiers) == 0:
		return "Must provide either flat prices (inputPrice/outputPrice/totalPrice) OR pricingTiers"
	}
	if _, err := regexp.Compile(in.MatchPattern); err != nil {
		return "matchPattern is not a valid regex pattern (Postgres)"
	}
	defaults := 0
	for _, t := range in.PricingTiers {
		if t.IsDefault {
			defaults++
		}
		if len(t.Prices) == 0 {
			return fmt.Sprintf("Pricing tier %q must have at least one price defined", t.Name)
		}
	}
	if defaults != 1 {
		return "Exactly one pricing tier must have isDefault: true"
	}
	return ""
}
