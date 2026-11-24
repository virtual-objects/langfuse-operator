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
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	langfusev1alpha1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
)

// LangfuseConnectionReconciler verifies that a connection's credentials work
// and records which Langfuse project they belong to.
type LangfuseConnectionReconciler struct {
	client.Client
	Resolver     *ConnectionResolver
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=langfuse.virtual-objects.github.io,resources=langfuseconnections,verbs=get;list;watch
// +kubebuilder:rbac:groups=langfuse.virtual-objects.github.io,resources=langfuseconnections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile checks the connection against Langfuse.
func (r *LangfuseConnectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var conn langfusev1alpha1.LangfuseConnection
	if err := r.Get(ctx, req.NamespacedName, &conn); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	orig := conn.DeepCopy()

	result, reconcileErr := r.check(ctx, &conn)
	conn.Status.ObservedGeneration = conn.Generation

	if !equality.Semantic.DeepEqual(orig.Status, conn.Status) {
		if err := r.Status().Patch(ctx, &conn, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status: %w", err)
		}
	}
	if reconcileErr != nil {
		log.Error(reconcileErr, "connection check failed")
	}
	return result, reconcileErr
}

func (r *LangfuseConnectionReconciler) check(ctx context.Context, conn *langfusev1alpha1.LangfuseConnection) (ctrl.Result, error) {
	lf, err := r.Resolver.Client(ctx, conn)
	if err != nil {
		var ce *connectionError
		if errors.As(err, &ce) {
			setReady(&conn.Status.Conditions, conn.Generation, false, ce.reason, ce.Error())
			return ctrl.Result{RequeueAfter: retryShort}, nil
		}
		setReady(&conn.Status.Conditions, conn.Generation, false, langfusev1alpha1.ReasonAPIError, err.Error())
		return ctrl.Result{}, err
	}

	project, err := lf.GetProject(ctx)
	switch {
	case langfuse.IsUnauthorized(err):
		setReady(&conn.Status.Conditions, conn.Generation, false, langfusev1alpha1.ReasonUnauthorized,
			"Langfuse rejected the API key; use a project-scoped key pair: "+err.Error())
		return ctrl.Result{RequeueAfter: retryLong}, nil
	case err != nil:
		setReady(&conn.Status.Conditions, conn.Generation, false, langfusev1alpha1.ReasonAPIError, err.Error())
		return ctrl.Result{}, err
	}

	conn.Status.ProjectID = project.ID
	conn.Status.ProjectName = project.Name
	setReady(&conn.Status.Conditions, conn.Generation, true, langfusev1alpha1.ReasonSynced,
		fmt.Sprintf("Connected to project %q", project.Name))
	return ctrl.Result{RequeueAfter: r.resync()}, nil
}

func (r *LangfuseConnectionReconciler) resync() time.Duration {
	if r.ResyncPeriod > 0 {
		return r.ResyncPeriod
	}
	return DefaultResyncPeriod
}

// SetupWithManager sets up the controller with the Manager.
func (r *LangfuseConnectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&langfusev1alpha1.LangfuseConnection{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("langfuseconnection").
		Complete(r)
}
