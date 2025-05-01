// SPDX-FileCopyrightText: 2025 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// SecretReconciler reconciles secrets with the proper source annotation.
type SecretReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	SourceAnnotation     string
	TargetNameAnnotation string
	Finalizer            string
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *SecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Starting reconcile")

	source := &corev1.Secret{}
	if err := r.Get(ctx, req.NamespacedName, source); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if tls.crt and tls.key are set in the source secret
	if source.Data["tls.crt"] == nil ||
		len(source.Data["tls.crt"]) == 0 ||
		source.Data["tls.key"] == nil ||
		len(source.Data["tls.crt"]) == 0 {
		log.Error(nil, "Source secret does not contain tls.crt and tls.key")
		return ctrl.Result{}, nil
	}

	// Get and update target
	target := &corev1.Secret{}
	targetNamespacedName := types.NamespacedName{
		Namespace: source.Namespace,
		Name:      source.Annotations["rotator.gw.ei.telekom.de/destination-secret-name"],
	}
	log = log.WithValues("target", targetNamespacedName)
	err := r.Get(ctx, targetNamespacedName, target)
	targetExists := true
	if errors.IsNotFound(err) {
		targetExists = false
	} else if err != nil {
		log.Error(err, "Failed to get target secret")
		return ctrl.Result{}, err
	}

	if source.ObjectMeta.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(source, r.Finalizer) {
		// Source is not being deleted, add finalizer if not present
		log.Info("Adding finalizer to source secret")
		controllerutil.AddFinalizer(source, r.Finalizer)
		if err = r.Update(ctx, source); err != nil {
			return ctrl.Result{}, err
		}
	} else if !source.ObjectMeta.DeletionTimestamp.IsZero() {
		return handleDeletion(ctx, r, source, target, targetExists)
	}

	// Calculate kid
	kid := uuid.NewSHA1(uuid.Nil, source.Data["tls.crt"])

	if !targetExists { //nolint:nestif // would be more complex if it was in separate method
		// Target doesn't exist -> initialize it
		target := initializeLocalTarget(source, kid)

		if err = controllerutil.SetControllerReference(source, &target, r.Scheme); err != nil {
			log.Error(err, "Failed to set controller reference")
			return ctrl.Result{}, err
		}

		if err = r.Create(ctx, &target); err != nil {
			log.Error(err, "Failed to create target secret")
			return ctrl.Result{}, err
		}
		log.Info("Successfully created target secret")
	} else {
		// Target does exist -> rotate values
		// Don't rotate if source is equal to next-tls
		if string(source.Data["tls.crt"]) == string(target.Data["next-tls.crt"]) {
			log.Info("Skipping update, source certificate is equal to certificate in target/next-tls.crt")
			return ctrl.Result{}, nil
		}

		log.Info("Updating target secret with rotated values")
		updateLocalTargetData(target, source, kid)

		if err = controllerutil.SetControllerReference(source, target, r.Scheme); err != nil {
			log.Error(err, "Failed to set controller reference")
			return ctrl.Result{}, err
		}

		// Update the target secret
		if err = r.Update(ctx, target); err != nil {
			log.Error(err, "Failed to update target secret")
			return ctrl.Result{}, err
		}
		log.Info("Successfully updated target secret with rotated values")
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
// It filters the events to only those secrets with the source annotation.
func (r *SecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	secretPredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return false
		}

		sourceVal, sourceExists := secret.Annotations[r.SourceAnnotation]
		targetNameVal, targetNameExists := secret.Annotations[r.TargetNameAnnotation]
		return sourceExists && sourceVal == "true" && targetNameExists && len(targetNameVal) > 0
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}).
		Named("key-secret").
		WithEventFilter(secretPredicate).
		Owns(&corev1.Secret{}).
		Complete(r)
}

// initializeLocalTarget initializes a target secret with the given source secret and kid in the next-tls.* fields.
// It does not create the secret in the cluster.
func initializeLocalTarget(source *corev1.Secret, kid uuid.UUID) corev1.Secret {
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      source.Annotations["rotator.gw.ei.telekom.de/destination-secret-name"],
			Namespace: source.Namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"next-tls.crt": source.Data["tls.crt"],
			"next-tls.key": source.Data["tls.key"],
			"next-tls.kid": []byte(kid.String()),
			"tls.crt":      {},
			"tls.key":      {},
			"tls.kid":      {},
			"prev-tls.crt": {},
			"prev-tls.key": {},
			"prev-tls.kid": {},
		},
	}
}

// updateLocalTargetData updates the target secret with the given source secret and kid by moving the
// - values from the tls.* fields to the prev-tls.* fields
// - the values from the next-tls.* fields to the tls.*. fields
// - the values from the source secret to the next-tls.* fields (and generating a new kid)
// It does not update the secret in the cluster.
func updateLocalTargetData(target *corev1.Secret, source *corev1.Secret, kid uuid.UUID) {
	// Create updated data map
	updatedData := map[string][]byte{
		// Move tls to prev-tls
		"prev-tls.crt": target.Data["tls.crt"],
		"prev-tls.key": target.Data["tls.key"],
		"prev-tls.kid": target.Data["tls.kid"],

		// Move new-tls to tls
		"tls.crt": target.Data["next-tls.crt"],
		"tls.key": target.Data["next-tls.key"],
		"tls.kid": target.Data["next-tls.kid"],

		// Copy source secret data to next-tls
		"next-tls.crt": source.Data["tls.crt"],
		"next-tls.key": source.Data["tls.key"],
		"next-tls.kid": []byte(kid.String()),
	}

	// Update the target secret
	target.Data = updatedData
}

// handleDeletion prevents garbage collection of target secret if the source secret is being deleted.
func handleDeletion(
	ctx context.Context,
	r *SecretReconciler,
	source *corev1.Secret,
	target *corev1.Secret,
	targetExists bool) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(source, r.Finalizer) {
		log.Info("Source secret is marked for deletion and doesn't have a finalizer. Skipping.")
		return ctrl.Result{}, nil
	}

	log.Info("Source secret is under deletion. Keeping target and removing owner reference")
	if targetExists {
		// Remove the owner reference so the target continues to exist without the source
		err := controllerutil.RemoveOwnerReference(source, target, r.Scheme)
		if err != nil {
			log.Error(err, "Failed to remove owner reference")
			return ctrl.Result{}, err
		}
		// Remove deletion timestamp to prevent deletion
		target.SetDeletionTimestamp(nil)
		if err = r.Update(ctx, target); err != nil {
			log.Error(err, "Failed to remove the deletion timestamp")
			return ctrl.Result{}, err
		}
	}
	// Remove the finalizer
	controllerutil.RemoveFinalizer(source, r.Finalizer)
	if err := r.Update(ctx, source); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
