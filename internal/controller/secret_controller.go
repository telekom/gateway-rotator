/*
Copyright 2025.

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

// SecretReconciler reconciles secrets with the proper source annotation
type SecretReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	SourceAnnotation     string
	TargetNameAnnotation string
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
	val, isSourceSecret := source.Annotations[r.SourceAnnotation]
	if !isSourceSecret || val != "true" {
		// This is likely a target secret or some other secret we don't want to reconcile
		log.Info("Skipping reconciliation for non-source secret")
		return ctrl.Result{}, nil
	}

	target := &corev1.Secret{}
	targetExists := false
	targetNamespacedName := types.NamespacedName{
		Namespace: source.Namespace,
		Name:      source.Annotations["rotator.gateway.mdw.telekom.de/destination-secret-name"],
	}
	err := r.Get(ctx, targetNamespacedName, target)
	if err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to get target secret")
		return ctrl.Result{}, err
	}
	if errors.IsNotFound(err) {
		// Target doesn't exist -> initialize it
		targetExists = false
		target = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      targetNamespacedName.Name,
				Namespace: source.Namespace,
			},
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{
				"new-tls.crt": source.Data["tls.crt"],
				"new-tls.key": source.Data["tls.key"],
				"tls.crt":     {},
				"tls.key":     {},
				"old-tls.crt": {},
				"old-tls.key": {},
			},
		}
	} else {
		// Target does exist -> rotate values
		targetExists = true
		log.Info("Updating target secret with rotated values")

		// Create updated data map
		updatedData := map[string][]byte{
			// Move tls to old-tls
			"old-tls.crt": target.Data["tls.crt"],
			"old-tls.key": target.Data["tls.key"],

			// Move new-tls to tls
			"tls.crt": target.Data["new-tls.crt"],
			"tls.key": target.Data["new-tls.key"],

			// Copy source secret data to new-tls
			"new-tls.crt": source.Data["tls.crt"],
			"new-tls.key": source.Data["tls.key"],
		}

		// Update the target secret
		target.Data = updatedData
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(source, target, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference")
		return ctrl.Result{}, err
	}

	if targetExists {
		if err := r.Update(ctx, target); err != nil {
			log.Error(err, "Failed to update target secret")
			return ctrl.Result{}, err
		}
	} else {
		if err := r.Create(ctx, target); err != nil {
			log.Error(err, "Failed to create target secret")
			return ctrl.Result{}, err
		}
	}

	log.Info("Successfully updated target secret with rotated values")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SecretReconciler) SetupWithManager(mgr ctrl.Manager) error {

	secretPredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return false
		}

		val, exists := secret.Annotations[r.TargetNameAnnotation]
		return exists && val == "true"
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}).
		Named("key-secret").
		WithEventFilter(secretPredicate).
		Owns(&corev1.Secret{}).
		Complete(r)
}
