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

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	langfusev1alpha1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
)

// DefaultResyncPeriod is how often Ready objects are re-checked against Langfuse
// to correct drift and pick up rotated credentials.
const DefaultResyncPeriod = 10 * time.Minute

const (
	// Finalizer guards deletion of objects that own something in Langfuse.
	Finalizer = "langfuse.virtual-objects.github.io/finalizer"

	retryShort = 30 * time.Second
	retryLong  = 5 * time.Minute
)

// ClientFactory builds a Langfuse client. Tests and other kinds can swap it.
type ClientFactory func(host, publicKey, secretKey string) (*langfuse.Client, error)

// DefaultClientFactory returns a client with default settings.
func DefaultClientFactory(host, publicKey, secretKey string) (*langfuse.Client, error) {
	return langfuse.New(host, publicKey, secretKey)
}

// connectionError carries a Ready reason alongside the error.
type connectionError struct {
	reason string
	err    error
}

func (e *connectionError) Error() string { return e.err.Error() }
func (e *connectionError) Unwrap() error { return e.err }

// ConnectionResolver turns a LangfuseConnection into an authenticated client.
// Secrets are read with an uncached reader, so the operator only needs "get"
// on Secrets and never caches Secrets cluster-wide.
type ConnectionResolver struct {
	SecretReader client.Reader
	NewClient    ClientFactory
}

// Client returns a Langfuse client for conn.
func (r *ConnectionResolver) Client(ctx context.Context, conn *langfusev1alpha1.LangfuseConnection) (*langfuse.Client, error) {
	ref := conn.Spec.APIKeySecretRef
	pkKey, skKey := ref.PublicKeyKey, ref.SecretKeyKey
	if pkKey == "" {
		pkKey = "public_key"
	}
	if skKey == "" {
		skKey = "secret_key"
	}

	var secret corev1.Secret
	if err := r.SecretReader.Get(ctx, types.NamespacedName{Namespace: conn.Namespace, Name: ref.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &connectionError{langfusev1alpha1.ReasonSecretNotFound, fmt.Errorf("secret %q not found", ref.Name)}
		}
		return nil, fmt.Errorf("get secret %q: %w", ref.Name, err)
	}
	pk, sk := string(secret.Data[pkKey]), string(secret.Data[skKey])
	if pk == "" || sk == "" {
		return nil, &connectionError{langfusev1alpha1.ReasonSecretNotFound,
			fmt.Errorf("secret %q must contain non-empty keys %q and %q", ref.Name, pkKey, skKey)}
	}
	factory := r.NewClient
	if factory == nil {
		factory = DefaultClientFactory
	}
	c, err := factory(conn.Spec.Host, pk, sk)
	if err != nil {
		return nil, &connectionError{langfusev1alpha1.ReasonInvalidSpec, err}
	}
	return c, nil
}

// setReady sets the Ready condition.
func setReady(conds *[]metav1.Condition, generation int64, ok bool, reason, msg string) {
	status := metav1.ConditionFalse
	if ok {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               langfusev1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            truncate(msg, 1024),
		ObservedGeneration: generation,
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// isConnectionReady reports whether a LangfuseConnection can be used.
func isConnectionReady(conn *langfusev1alpha1.LangfuseConnection) bool {
	return conn.Status.ProjectID != "" &&
		conn.Status.ObservedGeneration == conn.Generation &&
		meta.IsStatusConditionTrue(conn.Status.Conditions, langfusev1alpha1.ConditionReady)
}
