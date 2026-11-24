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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	langfusev1alpha1 "github.com/virtual-objects/langfuse-operator/api/v1alpha1"
	"github.com/virtual-objects/langfuse-operator/internal/langfuse"
	"github.com/virtual-objects/langfuse-operator/internal/modeldef"
)

const (
	indexModelConnection = "spec.connectionRef.name"
	indexModelIdentity   = "spec.connectionRef.name+spec.modelName"
)

func modelIdentity(m *langfusev1alpha1.LangfuseModel) string {
	return m.Spec.ConnectionRef.Name + "/" + m.Spec.ModelName
}

// LangfuseModelReconciler keeps a Langfuse model definition in line with a LangfuseModel.
type LangfuseModelReconciler struct {
	client.Client
	Resolver     *ConnectionResolver
	Recorder     record.EventRecorder
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=langfuse.virtual-objects.github.io,resources=langfusemodels,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=langfuse.virtual-objects.github.io,resources=langfusemodels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=langfuse.virtual-objects.github.io,resources=langfusemodels/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile creates, recreates or deletes the model in Langfuse.
func (r *LangfuseModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var model langfusev1alpha1.LangfuseModel
	if err := r.Get(ctx, req.NamespacedName, &model); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !model.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &model)
	}
	if controllerutil.AddFinalizer(&model, Finalizer) {
		if err := r.Update(ctx, &model); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
	}

	orig := model.DeepCopy()
	result, reconcileErr := r.sync(ctx, &model)
	model.Status.ObservedGeneration = model.Generation
	if !equality.Semantic.DeepEqual(orig.Status, model.Status) {
		if err := r.Status().Patch(ctx, &model, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status: %w", err)
		}
	}
	return result, reconcileErr
}

func (r *LangfuseModelReconciler) notReady(m *langfusev1alpha1.LangfuseModel, reason, msg string) {
	setReady(&m.Status.Conditions, m.Generation, false, reason, msg)
}

func (r *LangfuseModelReconciler) sync(ctx context.Context, m *langfusev1alpha1.LangfuseModel) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if owner, err := r.conflictingOwner(ctx, m); err != nil {
		return ctrl.Result{}, err
	} else if owner != "" {
		r.notReady(m, langfusev1alpha1.ReasonConflict, fmt.Sprintf(
			"LangfuseModel %q already manages model %q through connection %q", owner, m.Spec.ModelName, m.Spec.ConnectionRef.Name))
		return ctrl.Result{RequeueAfter: retryLong}, nil
	}

	want, err := modeldef.Desired(&m.Spec)
	if err != nil {
		r.notReady(m, langfusev1alpha1.ReasonInvalidSpec, err.Error())
		return ctrl.Result{}, nil
	}

	conn, lf, res, err := r.connect(ctx, m)
	if lf == nil {
		return res, err
	}

	// The connection now points at another project: the old model is out of reach.
	if m.Status.ProjectID != "" && m.Status.ProjectID != conn.Status.ProjectID {
		r.Recorder.Eventf(m, corev1.EventTypeWarning, "ProjectChanged",
			"Connection now targets project %s; model %s in project %s is no longer managed",
			conn.Status.ProjectID, m.Status.ModelID, m.Status.ProjectID)
		m.Status.ModelID = ""
	}
	m.Status.ProjectID = conn.Status.ProjectID

	existing, err := r.findExisting(ctx, lf, m, want.ModelName)
	if err != nil {
		return r.apiFailure(m, err)
	}

	action := ""
	if existing != nil {
		diff := modeldef.Diff(want, existing)
		if diff == "" {
			m.Status.ModelID = existing.ID
			setReady(&m.Status.Conditions, m.Generation, true, langfusev1alpha1.ReasonSynced,
				fmt.Sprintf("Model %s is up to date in project %q", existing.ID, conn.Status.ProjectName))
			return ctrl.Result{RequeueAfter: r.resync()}, nil
		}
		// Langfuse v3 has no update endpoint, so drift is fixed by delete + create.
		log.Info("recreating model", "modelID", existing.ID, "diff", diff)
		if err := lf.DeleteModel(ctx, existing.ID); err != nil && !langfuse.IsNotFound(err) {
			return r.apiFailure(m, err)
		}
		m.Status.ModelID = ""
		action = "Recreated"
		r.Recorder.Eventf(m, corev1.EventTypeNormal, "Deleted", "Deleted model %s to apply change: %s", existing.ID, diff)
	}

	created, err := lf.CreateModel(ctx, want)
	if err != nil {
		return r.apiFailure(m, err)
	}
	if action == "" {
		action = "Created"
	}
	m.Status.ModelID = created.ID
	r.Recorder.Eventf(m, corev1.EventTypeNormal, action, "%s model %s (%s) in project %q",
		action, created.ID, created.ModelName, conn.Status.ProjectName)
	setReady(&m.Status.Conditions, m.Generation, true, langfusev1alpha1.ReasonSynced,
		fmt.Sprintf("Model %s is up to date in project %q", created.ID, conn.Status.ProjectName))
	return ctrl.Result{RequeueAfter: r.resync()}, nil
}

// connect returns the ready connection and a client, or a result to return.
func (r *LangfuseModelReconciler) connect(ctx context.Context, m *langfusev1alpha1.LangfuseModel) (
	*langfusev1alpha1.LangfuseConnection, *langfuse.Client, ctrl.Result, error) {
	var conn langfusev1alpha1.LangfuseConnection
	key := types.NamespacedName{Namespace: m.Namespace, Name: m.Spec.ConnectionRef.Name}
	if err := r.Get(ctx, key, &conn); err != nil {
		if apierrors.IsNotFound(err) {
			r.notReady(m, langfusev1alpha1.ReasonConnectionNotReady,
				fmt.Sprintf("LangfuseConnection %q not found", key.Name))
			return nil, nil, ctrl.Result{}, nil
		}
		return nil, nil, ctrl.Result{}, err
	}
	if !isConnectionReady(&conn) {
		r.notReady(m, langfusev1alpha1.ReasonConnectionNotReady,
			fmt.Sprintf("LangfuseConnection %q is not ready", key.Name))
		return nil, nil, ctrl.Result{}, nil
	}
	lf, err := r.Resolver.Client(ctx, &conn)
	if err != nil {
		var ce *connectionError
		if errors.As(err, &ce) {
			r.notReady(m, langfusev1alpha1.ReasonConnectionNotReady, err.Error())
			return nil, nil, ctrl.Result{RequeueAfter: retryShort}, nil
		}
		return nil, nil, ctrl.Result{}, err
	}
	return &conn, lf, ctrl.Result{}, nil
}

// findExisting returns the project model this resource manages, adopting an
// existing project model with the same name. Built-in models are never touched;
// a project model with the same name overrides them.
func (r *LangfuseModelReconciler) findExisting(ctx context.Context, lf *langfuse.Client,
	m *langfusev1alpha1.LangfuseModel, name string) (*langfuse.Model, error) {
	if id := m.Status.ModelID; id != "" {
		got, err := lf.GetModel(ctx, id)
		switch {
		case langfuse.IsNotFound(err):
		case err != nil:
			return nil, err
		case got.IsLangfuseManaged:
		case got.ModelName == name:
			return got, nil
		default:
			// modelName changed: remove the model stored under the old name.
			if err := lf.DeleteModel(ctx, id); err != nil && !langfuse.IsNotFound(err) {
				return nil, err
			}
			r.Recorder.Eventf(m, corev1.EventTypeNormal, "Deleted", "Deleted model %s (%s) after rename", id, got.ModelName)
		}
		m.Status.ModelID = ""
	}

	models, err := lf.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		if !models[i].IsLangfuseManaged && models[i].ModelName == name {
			r.Recorder.Eventf(m, corev1.EventTypeNormal, "Adopted", "Adopted existing model %s (%s)", models[i].ID, name)
			return &models[i], nil
		}
	}
	return nil, nil
}

// apiFailure records a Langfuse API error and decides how to retry.
func (r *LangfuseModelReconciler) apiFailure(m *langfusev1alpha1.LangfuseModel, err error) (ctrl.Result, error) {
	switch {
	case langfuse.IsUnauthorized(err):
		r.notReady(m, langfusev1alpha1.ReasonUnauthorized, err.Error())
		return ctrl.Result{RequeueAfter: retryLong}, nil
	case langfuse.IsRejected(err):
		r.notReady(m, langfusev1alpha1.ReasonRejected, err.Error())
		r.Recorder.Event(m, corev1.EventTypeWarning, "Rejected", truncate(err.Error(), 1024))
		return ctrl.Result{RequeueAfter: retryLong}, nil
	default:
		r.notReady(m, langfusev1alpha1.ReasonAPIError, err.Error())
		return ctrl.Result{}, err
	}
}

// conflictingOwner returns the name of an older LangfuseModel in the same
// namespace that manages the same modelName through the same connection.
func (r *LangfuseModelReconciler) conflictingOwner(ctx context.Context, m *langfusev1alpha1.LangfuseModel) (string, error) {
	var list langfusev1alpha1.LangfuseModelList
	if err := r.List(ctx, &list, client.InNamespace(m.Namespace),
		client.MatchingFields{indexModelIdentity: modelIdentity(m)}); err != nil {
		return "", fmt.Errorf("list models: %w", err)
	}
	for i := range list.Items {
		o := &list.Items[i]
		if o.UID == m.UID || !o.DeletionTimestamp.IsZero() {
			continue
		}
		older := o.CreationTimestamp.Before(&m.CreationTimestamp) ||
			(o.CreationTimestamp.Equal(&m.CreationTimestamp) && o.Name < m.Name)
		if older {
			return o.Name, nil
		}
	}
	return "", nil
}

func (r *LangfuseModelReconciler) finalize(ctx context.Context, m *langfusev1alpha1.LangfuseModel) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(m, Finalizer) {
		return ctrl.Result{}, nil
	}
	if m.Spec.DeletionPolicy != langfusev1alpha1.DeletionPolicyOrphan && m.Status.ModelID != "" {
		res, done, err := r.deleteRemote(ctx, m)
		if !done {
			return res, err
		}
	}
	controllerutil.RemoveFinalizer(m, Finalizer)
	if err := r.Update(ctx, m); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

// deleteRemote deletes the Langfuse model. done=false means retry later.
func (r *LangfuseModelReconciler) deleteRemote(ctx context.Context, m *langfusev1alpha1.LangfuseModel) (ctrl.Result, bool, error) {
	var conn langfusev1alpha1.LangfuseConnection
	err := r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Spec.ConnectionRef.Name}, &conn)
	if apierrors.IsNotFound(err) || (err == nil && !conn.DeletionTimestamp.IsZero()) {
		// Without a connection the model cannot be reached; do not block deletion.
		r.Recorder.Eventf(m, corev1.EventTypeWarning, "Orphaned",
			"LangfuseConnection %q is gone; model %s was left in Langfuse", m.Spec.ConnectionRef.Name, m.Status.ModelID)
		return ctrl.Result{}, true, nil
	}
	if err != nil {
		return ctrl.Result{}, false, err
	}
	if conn.Status.ProjectID != "" && m.Status.ProjectID != "" && conn.Status.ProjectID != m.Status.ProjectID {
		r.Recorder.Eventf(m, corev1.EventTypeWarning, "Orphaned",
			"Connection now targets another project; model %s was left in project %s", m.Status.ModelID, m.Status.ProjectID)
		return ctrl.Result{}, true, nil
	}
	lf, err := r.Resolver.Client(ctx, &conn)
	if err != nil {
		var ce *connectionError
		if errors.As(err, &ce) {
			r.Recorder.Eventf(m, corev1.EventTypeWarning, "DeleteBlocked", "Cannot delete model %s: %v", m.Status.ModelID, err)
			return ctrl.Result{RequeueAfter: retryShort}, false, nil
		}
		return ctrl.Result{}, false, err
	}
	if err := lf.DeleteModel(ctx, m.Status.ModelID); err != nil && !langfuse.IsNotFound(err) {
		r.Recorder.Eventf(m, corev1.EventTypeWarning, "DeleteFailed", "Cannot delete model %s: %v", m.Status.ModelID, err)
		if langfuse.IsUnauthorized(err) {
			return ctrl.Result{RequeueAfter: retryLong}, false, nil
		}
		return ctrl.Result{}, false, err
	}
	r.Recorder.Eventf(m, corev1.EventTypeNormal, "Deleted", "Deleted model %s from Langfuse", m.Status.ModelID)
	return ctrl.Result{}, true, nil
}

func (r *LangfuseModelReconciler) resync() time.Duration {
	if r.ResyncPeriod > 0 {
		return r.ResyncPeriod
	}
	return DefaultResyncPeriod
}

// SetupWithManager sets up the controller with the Manager.
func (r *LangfuseModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	idx := mgr.GetFieldIndexer()
	if err := idx.IndexField(context.Background(), &langfusev1alpha1.LangfuseModel{}, indexModelConnection,
		func(o client.Object) []string {
			return []string{o.(*langfusev1alpha1.LangfuseModel).Spec.ConnectionRef.Name}
		}); err != nil {
		return err
	}
	if err := idx.IndexField(context.Background(), &langfusev1alpha1.LangfuseModel{}, indexModelIdentity,
		func(o client.Object) []string {
			return []string{modelIdentity(o.(*langfusev1alpha1.LangfuseModel))}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&langfusev1alpha1.LangfuseModel{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&langfusev1alpha1.LangfuseConnection{}, handler.EnqueueRequestsFromMapFunc(r.modelsForConnection)).
		Named("langfusemodel").
		Complete(r)
}

func (r *LangfuseModelReconciler) modelsForConnection(ctx context.Context, o client.Object) []reconcile.Request {
	var list langfusev1alpha1.LangfuseModelList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace()),
		client.MatchingFields{indexModelConnection: o.GetName()}); err != nil {
		logf.FromContext(ctx).Error(err, "list models for connection", "connection", o.GetName())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, m := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: m.Namespace, Name: m.Name}})
	}
	return reqs
}
