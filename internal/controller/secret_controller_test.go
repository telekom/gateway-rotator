// SPDX-FileCopyrightText: 2025 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gw.mdw.telekom.de/rotator/internal/controller"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func generateUuid(input string) []byte {
	return []byte(uuid.NewSHA1(uuid.Nil, []byte(input)).String())
}

var _ = Describe("Secret Controller", Serial, func() {
	var source *corev1.Secret = &corev1.Secret{}
	var target *corev1.Secret = &corev1.Secret{}

	const (
		timeout  = time.Second * 10
		duration = time.Second * 10
		interval = time.Millisecond * 250
	)

	AfterEach(func() {
		// delete secrets
		err := k8sClient.DeleteAllOf(ctx, &corev1.Secret{}, client.InNamespace(namespace))
		Expect(err).NotTo(HaveOccurred(), "deletion of secret failed during cleanup")
		Eventually(func(g Gomega) {
			secrets := &corev1.SecretList{}
			err = k8sClient.List(ctx, secrets, client.InNamespace(namespace))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(secrets.Items).To(BeEmpty())

		}, timeout, interval).Should(Succeed(), "secrets were not deleted within timeout during cleanup")
	})

	When("a source secret is created", func() {
		BeforeEach(func() {
			source = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"rotator.gateway.mdw.telekom.de/source":                  "true",
						"rotator.gateway.mdw.telekom.de/destination-secret-name": "target",
					},
					Name:      "source",
					Namespace: namespace,
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"tls.crt": []byte("cert"),
					"tls.key": []byte("key"),
				},
			}
			Expect(k8sClient.Create(ctx, source)).To(Succeed(), "creation of source secret failed")

			// wait for the target secret to be created
			Eventually(func(g Gomega) {
				err := k8sClient.Get(
					ctx,
					types.NamespacedName{Name: "target", Namespace: namespace},
					target,
				)
				g.Expect(err).ShouldNot(HaveOccurred())
			}, timeout, interval).Should(Succeed(), "controller did not create target secret within timeout")
		})

		It("creates a target secret with the correct name and namespace", func() {
			By("setting the key and crt from the source in next-tls.*", func() {
				Expect(target.Data["next-tls.crt"]).To(Equal([]byte("cert")))
				Expect(target.Data["next-tls.key"]).To(Equal([]byte("key")))
			})

			By("leaving tls.* and prev-tls.* empty", func() {
				Expect(target.Data["tls.crt"]).To(BeEmpty())
				Expect(target.Data["tls.key"]).To(BeEmpty())
				Expect(target.Data["tls.kid"]).To(BeEmpty())
				Expect(target.Data["prev-tls.crt"]).To(BeEmpty())
				Expect(target.Data["prev-tls.key"]).To(BeEmpty())
				Expect(target.Data["prev-tls.kid"]).To(BeEmpty())
			})

			By("generating a UUID based on the cert and setting it as a kid", func() {
				Expect(
					target.Data["next-tls.kid"],
				).To(Equal(generateUuid(string(source.Data["tls.crt"]))))
			})

			By("setting secret type tls", func() {
				Expect(target.Type).To(Equal(corev1.SecretTypeTLS))
			})

			By("setting an owner reference to the source secret", func() {
				Expect(target.OwnerReferences).To(HaveLen(1))
				Expect(target.OwnerReferences[0].Name).To(Equal("source"))
				Expect(target.OwnerReferences[0].Kind).To(Equal("Secret"))
			})
		})

		Context("and the source secret changes", func() {
			Context("and the cert in source and next-tls.crt are equal", func() {
				BeforeEach(func() {
					// we will just update an annotation to trigger a change
					err := k8sClient.Get(
						ctx,
						types.NamespacedName{Name: "source", Namespace: namespace},
						source,
					)
					Expect(err).ToNot(HaveOccurred())
					source.Annotations["some-new-annotation"] = "some-new-value"
					Expect(
						k8sClient.Update(ctx, source),
					).To(Succeed(), "update of source secret failed")
				})
				It("does not update the target secret", func() {
					time.Sleep(time.Second * 2) // give controller a chance to reconcile
					Consistently(func(g Gomega) {
						g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "target", Namespace: namespace}, target)).
							To(Succeed())
						_, ok := target.Annotations["some-new-annotation"]
						g.Expect(ok).To(BeFalse(), "target secret should not have been touched")
					}, timeout, interval).Should(Succeed(), "the target secret should not have been updated")
				})
			})

			Context("and the cert in source and next-tls.crt are different", func() {
				It("is able to perform a full rotation", func() {
					By("(1) manually changing the source", func() {
						err := k8sClient.Get(
							ctx,
							types.NamespacedName{Name: "source", Namespace: namespace},
							source,
						)
						Expect(err).ToNot(HaveOccurred())
						source.Data["tls.crt"] = []byte("cert-rotation-1")
						source.Data["tls.key"] = []byte("key-rotation-1")
						Expect(
							k8sClient.Update(ctx, source),
						).To(Succeed(), "update of source secret by test runner failed")
					})

					By("(1) the values are rotated", func() {
						Eventually(func(g Gomega) {
							g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "target", Namespace: namespace}, target)).
								To(Succeed())
							g.Expect(target.Data["next-tls.crt"]).
								To(Equal([]byte("cert-rotation-1")))
							g.Expect(target.Data["next-tls.key"]).
								To(Equal([]byte("key-rotation-1")))
							g.Expect(target.Data["next-tls.kid"]).
								To(Equal(generateUuid("cert-rotation-1")))
							g.Expect(target.Data["tls.crt"]).To(Equal([]byte("cert")))
							g.Expect(target.Data["tls.key"]).To(Equal([]byte("key")))
							g.Expect(target.Data["tls.kid"]).To(Equal(generateUuid("cert")))
							g.Expect(target.Data["prev-tls.crt"]).To(BeEmpty())
							g.Expect(target.Data["prev-tls.key"]).To(BeEmpty())
							g.Expect(target.Data["prev-tls.kid"]).To(BeEmpty())
						}, timeout, interval).Should(Succeed(), "controller did not produce the expected target secret within timeout")
					})

					By("(2) manually changing the source", func() {
						err := k8sClient.Get(
							ctx,
							types.NamespacedName{Name: "source", Namespace: namespace},
							source,
						)
						Expect(err).ToNot(HaveOccurred())
						source.Data["tls.crt"] = []byte("cert-rotation-2")
						source.Data["tls.key"] = []byte("key-rotation-2")
						Expect(
							k8sClient.Update(ctx, source),
						).To(Succeed(), "update of source secret by test runner failed")
					})

					By("(2) the values are rotated", func() {
						Eventually(func(g Gomega) {
							g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "target", Namespace: namespace}, target)).
								To(Succeed())
							g.Expect(target.Data["next-tls.crt"]).
								To(Equal([]byte("cert-rotation-2")))
							g.Expect(target.Data["next-tls.key"]).
								To(Equal([]byte("key-rotation-2")))
							g.Expect(target.Data["next-tls.kid"]).
								To(Equal(generateUuid("cert-rotation-2")))
							g.Expect(target.Data["tls.crt"]).To(Equal([]byte("cert-rotation-1")))
							g.Expect(target.Data["tls.key"]).To(Equal([]byte("key-rotation-1")))
							g.Expect(target.Data["tls.kid"]).
								To(Equal(generateUuid("cert-rotation-1")))
							g.Expect(target.Data["prev-tls.crt"]).To(Equal([]byte("cert")))
							g.Expect(target.Data["prev-tls.key"]).To(Equal([]byte("key")))
							g.Expect(target.Data["prev-tls.kid"]).To(Equal(generateUuid("cert")))
						}, timeout, interval).Should(Succeed(), "controller did not produce the expected target secret within timeout")
					})

					By("(3) manually changing the source", func() {
						err := k8sClient.Get(
							ctx,
							types.NamespacedName{Name: "source", Namespace: namespace},
							source,
						)
						Expect(err).ToNot(HaveOccurred())
						source.Data["tls.crt"] = []byte("cert-rotation-3")
						source.Data["tls.key"] = []byte("key-rotation-3")
						Expect(
							k8sClient.Update(ctx, source),
						).To(Succeed(), "update of source secret by test runner failed")
					})

					By("(3) the values are rotated", func() {
						Eventually(func(g Gomega) {
							g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "target", Namespace: namespace}, target)).
								To(Succeed())
							g.Expect(target.Data["next-tls.crt"]).
								To(Equal([]byte("cert-rotation-3")))
							g.Expect(target.Data["next-tls.key"]).
								To(Equal([]byte("key-rotation-3")))
							g.Expect(target.Data["next-tls.kid"]).
								To(Equal(generateUuid("cert-rotation-3")))
							g.Expect(target.Data["tls.crt"]).To(Equal([]byte("cert-rotation-2")))
							g.Expect(target.Data["tls.key"]).To(Equal([]byte("key-rotation-2")))
							g.Expect(target.Data["tls.kid"]).
								To(Equal(generateUuid("cert-rotation-2")))
							g.Expect(target.Data["prev-tls.crt"]).
								To(Equal([]byte("cert-rotation-1")))
							g.Expect(target.Data["prev-tls.key"]).
								To(Equal([]byte("key-rotation-1")))
							g.Expect(target.Data["prev-tls.kid"]).
								To(Equal(generateUuid("cert-rotation-1")))

						}, timeout, interval).Should(Succeed(), "controller did not produce the expected target secret within timeout")
					})
				})
			})
		})

		Context("and the source is deleted", func() {
			BeforeEach(func() {
				err := k8sClient.Delete(ctx, source)
				Expect(err).To(Succeed(), "deletion of source secret failed")
			})
			It("keeps the target secret", func() {
				Eventually(func(g Gomega) {
					err := k8sClient.Get(
						ctx,
						types.NamespacedName{Name: "target", Namespace: namespace},
						target,
					)
					g.Expect(err).ShouldNot(HaveOccurred())

					By("removing the owner reference")
					g.Expect(target.OwnerReferences).To(BeNil())

					By("removing the deletionTimestamp")
					g.Expect(target.DeletionTimestamp.IsZero()).To(BeTrue())
				}, timeout, interval).Should(Succeed(), "controller did not process target correctly")
			})
		})
	})

	When("a secret is created without the source and target-name annotations", func() {
		BeforeEach(func() {
			source = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-other-secret",
					Namespace: namespace,
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"tls.crt": []byte("cert"),
					"tls.key": []byte("key"),
				},
			}
			Expect(k8sClient.Create(ctx, source)).To(Succeed(), "creation of source secret failed")
		})
		It("does not reconcile the secret", func() {
			Eventually(func(g Gomega) {
				secrets := &corev1.SecretList{}
				err := k8sClient.List(ctx, secrets, client.InNamespace(namespace))
				Expect(err).ToNot(HaveOccurred(), "failed to list secrets")
				Expect(
					secrets.Items,
				).To(HaveLen(1), "controller should not have created a target secret")
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a source secret is created with empty tls.key or tls.crt values", func() {
		BeforeEach(func() {
			source = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"rotator.gateway.mdw.telekom.de/source":                  "true",
						"rotator.gateway.mdw.telekom.de/destination-secret-name": "target",
					},
					Name:      "source",
					Namespace: namespace,
				},
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"tls.crt": []byte(""),
					"tls.key": []byte(""),
				},
			}
			Expect(k8sClient.Create(ctx, source)).To(Succeed(), "creation of source secret failed")
		})
		It("does nothing", func() {
			Eventually(func(g Gomega) {
				secrets := &corev1.SecretList{}
				err := k8sClient.List(ctx, secrets, client.InNamespace(namespace))
				Expect(err).ToNot(HaveOccurred(), "failed to list secrets")
				Expect(
					secrets.Items,
				).To(HaveLen(1), "controller should not have created a target secret")
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a source secret is created without the expected fields", func() {
		BeforeEach(func() {
			source = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"rotator.gateway.mdw.telekom.de/source":                  "true",
						"rotator.gateway.mdw.telekom.de/destination-secret-name": "target",
					},
					Name:      "source",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"other-key": []byte("other-val"),
				},
			}
			Expect(k8sClient.Create(ctx, source)).To(Succeed(), "creation of source secret failed")
		})
		It("does nothing", func() {
			Eventually(func(g Gomega) {
				secrets := &corev1.SecretList{}
				err := k8sClient.List(ctx, secrets, client.InNamespace(namespace))
				Expect(err).ToNot(HaveOccurred(), "failed to list secrets")
				Expect(
					secrets.Items,
				).To(HaveLen(1), "controller should not have created a target secret")
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("error handling and edge cases", func() {
		var err error
		var erroringClient errorInjectingClient
		var reconciler controller.SecretReconciler
		JustBeforeEach(func() {
			erroringClient = errorInjectingClient{
				Client:       k8sClient,
				errorToThrow: err,
			}

			reconciler = controller.SecretReconciler{
				Client:               erroringClient,
				Scheme:               k8sClient.Scheme(),
				SourceAnnotation:     "rotator.gateway.mdw.telekom.de/source",
				TargetNameAnnotation: "rotator.gateway.mdw.telekom.de/destination-secret-name",
				Finalizer:            "rotator.gateway.mdw.telekom.de/finalizer",
			}
		})

		When("a reconcile is triggered and the resource is not found", func() {
			BeforeEach(func() {
				err = errors.NewNotFound(schema.GroupResource{}, "not found error")
			})
			It("returns no error and does nothing", func() {
				var val ctrl.Result
				val, err = reconciler.Reconcile(ctx, ctrl.Request{
					NamespacedName: types.NamespacedName{
						Name:      "test-secret",
						Namespace: "default",
					},
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(val).To(BeEquivalentTo(ctrl.Result{}))
				Eventually(func(g Gomega) {
					secrets := &corev1.SecretList{}
					err = k8sClient.List(ctx, secrets, client.InNamespace(namespace))
					g.Expect(err).ToNot(HaveOccurred(), "failed to list secrets")
					g.Expect(secrets.Items).
						To(BeEmpty(), "controller should not have created a target secret")
				}, timeout, interval).Should(Succeed())
			})
		})

		When("there is different error while fetching the resource", func() {
			BeforeEach(func() {
				err = errors.NewBadRequest("some undefined err")
			})
			It("returns the error and does nothing", func() {
				var val ctrl.Result
				val, err = reconciler.Reconcile(ctx, ctrl.Request{
					NamespacedName: types.NamespacedName{
						Name:      "test-secret",
						Namespace: "default",
					},
				})
				Expect(val).To(BeEquivalentTo(ctrl.Result{}))
				Expect(err).To(BeEquivalentTo(errors.NewBadRequest("some undefined err")))
				Eventually(func(g Gomega) {
					secrets := &corev1.SecretList{}
					err = k8sClient.List(ctx, secrets, client.InNamespace(namespace))
					g.Expect(err).ToNot(HaveOccurred(), "failed to list secrets")
					g.Expect(secrets.Items).
						To(BeEmpty(), "controller should not have created a target secret")
				}, timeout, interval).Should(Succeed())
			})
		})
	})

})

// Simple extension of the client.Client interface that is able to inject errors.
type errorInjectingClient struct {
	client.Client
	errorToThrow error
}

func (c errorInjectingClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	if c.errorToThrow != nil {
		return c.errorToThrow
	}
	return c.Client.Get(ctx, key, obj)
}
