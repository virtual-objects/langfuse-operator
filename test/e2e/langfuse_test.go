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

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
	"github.com/virtual-objects/langfuse-operator/test/utils"
)

// Values match test/e2e/langfuse/compose.yaml.
const (
	lfHostFromRunner  = "http://localhost:3000"
	lfHostFromCluster = "http://langfuse-web:3000"
	lfPublicKey       = "pk-lf-e2e"
	lfSecretKey       = "sk-lf-e2e"
	demoNamespace     = "langfuse-e2e-demo"
)

const demoManifests = `
apiVersion: v1
kind: Secret
metadata: {name: langfuse-project-keys, namespace: %[1]s}
stringData: {public_key: %[3]s, secret_key: %[4]s}
---
apiVersion: langfuse.virtual-objects.github.io/v1alpha1
kind: LangfuseConnection
metadata: {name: project, namespace: %[1]s}
spec:
  host: %[2]s
  apiKeySecretRef: {name: langfuse-project-keys}
---
apiVersion: langfuse.virtual-objects.github.io/v1alpha1
kind: LangfuseModel
metadata: {name: sonnet-geo, namespace: %[1]s}
spec:
  connectionRef: {name: project}
  modelName: e2e-claude-sonnet-4-6-bedrock-geo
  matchPattern: '(?i)^(us|eu|apac)\.anthropic\.claude-sonnet-4-6$'
  tokenizerId: claude
  pricingTiers:
    - name: Standard
      isDefault: true
      pricePerMillion: {input: "3.30", output: "16.50", cacheRead: "0.33", cacheWrite: "4.125"}
`

func kubectl(stdin string, args ...string) string {
	GinkgoHelper()
	cmd := exec.Command("kubectl", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	return out
}

func lfRequest(method, path string, body any, out any) error {
	var r *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, lfHostFromRunner+path, r)
	if err != nil {
		return err
	}
	req.SetBasicAuth(lfPublicKey, lfSecretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-From", "langfuse-operator-e2e")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func projectModels(lf *langfuse.Client) []langfuse.Model {
	GinkgoHelper()
	all, err := lf.ListModels(context.Background())
	Expect(err).NotTo(HaveOccurred())
	var own []langfuse.Model
	for _, m := range all {
		if !m.IsLangfuseManaged {
			own = append(own, m)
		}
	}
	return own
}

// langfuseScenario runs against the Langfuse started from test/e2e/langfuse/compose.yaml.
// It proves that the operator's model definition is what Langfuse uses to price
// a real generation.
func langfuseScenario() {
	if os.Getenv("LANGFUSE_E2E") != "true" {
		Skip("set LANGFUSE_E2E=true and start test/e2e/langfuse/compose.yaml (make test-e2e-langfuse)")
	}
	lf, err := langfuse.New(lfHostFromRunner, lfPublicKey, lfSecretKey)
	Expect(err).NotTo(HaveOccurred())

	By("waiting for Langfuse")
	Eventually(func() error {
		_, err := lf.GetProject(context.Background())
		return err
	}, 5*time.Minute, 5*time.Second).Should(Succeed())

	By("creating a Secret, LangfuseConnection and LangfuseModel")
	kubectl("", "create", "ns", demoNamespace)
	DeferCleanup(func() { _, _ = utils.Run(exec.Command("kubectl", "delete", "ns", demoNamespace, "--wait=false")) })
	kubectl(fmt.Sprintf(demoManifests, demoNamespace, lfHostFromCluster, lfPublicKey, lfSecretKey), "apply", "-f", "-")

	var modelID string
	Eventually(func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "-n", demoNamespace, "get", "lfmodel", "sonnet-geo",
			"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status} {.status.modelID}`))
		g.Expect(err).NotTo(HaveOccurred())
		parts := strings.Fields(out)
		g.Expect(parts).To(HaveLen(2), out)
		g.Expect(parts[0]).To(Equal("True"))
		modelID = parts[1]
	}).Should(Succeed())

	By("checking the model and its expanded prices in Langfuse")
	models := projectModels(lf)
	Expect(models).To(HaveLen(1))
	Expect(models[0].ID).To(Equal(modelID))
	Expect(models[0].PricingTiers[0].Prices).To(HaveLen(8))

	By("sending a Bedrock geo-profile generation and checking its cost")
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	genID := fmt.Sprintf("e2e-gen-%d", time.Now().UnixNano())
	Expect(lfRequest(http.MethodPost, "/api/public/ingestion", map[string]any{"batch": []map[string]any{
		{"id": genID + "-t", "type": "trace-create", "timestamp": ts, "body": map[string]any{"id": genID + "-trace"}},
		{"id": genID + "-g", "type": "generation-create", "timestamp": ts, "body": map[string]any{
			"id": genID, "traceId": genID + "-trace", "model": "us.anthropic.claude-sonnet-4-6",
			"startTime": ts, "endTime": ts,
			"usageDetails": map[string]int{
				"input": 1_000_000, "output": 100_000, "input_cache_read": 200_000, "input_cache_creation": 10_000,
			},
		}},
	}}, nil)).To(Succeed())

	// 1M*3.30/1M + 100k*16.50/1M + 200k*0.33/1M + 10k*4.125/1M
	const wantCost = 3.30 + 1.65 + 0.066 + 0.04125
	Eventually(func(g Gomega) {
		var obs struct {
			ModelID             string   `json:"modelId"`
			CalculatedTotalCost *float64 `json:"calculatedTotalCost"`
		}
		g.Expect(lfRequest(http.MethodGet, "/api/public/observations/"+genID, nil, &obs)).To(Succeed())
		g.Expect(obs.ModelID).To(Equal(modelID))
		g.Expect(obs.CalculatedTotalCost).NotTo(BeNil())
		g.Expect(math.Abs(*obs.CalculatedTotalCost - wantCost)).To(BeNumerically("<", 1e-6))
	}, 3*time.Minute, 3*time.Second).Should(Succeed())

	By("changing a price and seeing the model recreated")
	kubectl("", "-n", demoNamespace, "patch", "lfmodel", "sonnet-geo", "--type=json",
		"-p", `[{"op":"replace","path":"/spec/pricingTiers/0/pricePerMillion/output","value":"17"}]`)
	Eventually(func(g Gomega) {
		ms := projectModels(lf)
		g.Expect(ms).To(HaveLen(1))
		g.Expect(ms[0].ID).NotTo(Equal(modelID))
		g.Expect(string(ms[0].PricingTiers[0].Prices["output"])).To(Equal("0.000017"))
	}).Should(Succeed())

	By("deleting the LangfuseModel and seeing the model removed")
	kubectl("", "-n", demoNamespace, "delete", "lfmodel", "sonnet-geo", "--timeout=60s")
	Eventually(func() []langfuse.Model { return projectModels(lf) }).Should(BeEmpty())
}
