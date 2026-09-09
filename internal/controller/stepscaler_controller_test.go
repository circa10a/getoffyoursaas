package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	getoffyoursaasiov1alpha1 "github.com/circa10a/getoffyoursaas/api/v1alpha1"
)

var _ = Describe("StepScaler controller", func() {
	ctx := context.Background()
	const ns = "default"

	// No manager runs in this suite, so each spec drives the reconciler
	// directly. A FakeRecorder stands in for the manager's event recorder.
	newReconciler := func() *StepScalerReconciler {
		return &StepScalerReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(10),
		}
	}

	reconcileOnce := func(name string) {
		_, err := newReconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
		})
		Expect(err).NotTo(HaveOccurred())
	}

	createStepScaler := func(name, min, max string) {
		ss := &getoffyoursaasiov1alpha1.StepScaler{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: getoffyoursaasiov1alpha1.StepScalerSpec{
				ScaleTargetRef: autoscalingv1.CrossVersionObjectReference{
					APIVersion: "apps/v1", Kind: "Deployment", Name: name,
				},
				ContainerName: "*",
				DailyStepGoal: 10000,
				MinCPU:        resource.MustParse(min),
				MaxCPU:        resource.MustParse(max),
			},
		}
		Expect(k8sClient.Create(ctx, ss)).To(Succeed())
	}

	getVPA := func(name string) *vpav1.VerticalPodAutoscaler {
		vpa := &vpav1.VerticalPodAutoscaler{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, vpa)).To(Succeed())
		return vpa
	}

	minAllowedCPU := func(vpa *vpav1.VerticalPodAutoscaler) string {
		Expect(vpa.Spec.ResourcePolicy).NotTo(BeNil())
		Expect(vpa.Spec.ResourcePolicy.ContainerPolicies).To(HaveLen(1))
		q := vpa.Spec.ResourcePolicy.ContainerPolicies[0].MinAllowed[corev1.ResourceCPU]
		return q.String()
	}

	getStepScaler := func(name string) *getoffyoursaasiov1alpha1.StepScaler {
		ss := &getoffyoursaasiov1alpha1.StepScaler{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, ss)).To(Succeed())
		return ss
	}

	It("creates an owned VPA at the minimum floor when no steps are recorded", func() {
		createStepScaler("no-steps", "100m", "1000m")
		reconcileOnce("no-steps")

		vpa := getVPA("no-steps")
		Expect(minAllowedCPU(vpa)).To(Equal("100m"))

		By("owning the VPA so garbage collection cleans it up")
		Expect(vpa.OwnerReferences).To(HaveLen(1))
		Expect(vpa.OwnerReferences[0].Kind).To(Equal("StepScaler"))
		Expect(vpa.OwnerReferences[0].Name).To(Equal("no-steps"))
		Expect(vpa.OwnerReferences[0].Controller).ToNot(BeNil())
		Expect(*vpa.OwnerReferences[0].Controller).To(BeTrue())

		By("targeting the same workload as the StepScaler")
		Expect(vpa.Spec.TargetRef).NotTo(BeNil())
		Expect(vpa.Spec.TargetRef.Name).To(Equal("no-steps"))
		Expect(vpa.Spec.TargetRef.Kind).To(Equal("Deployment"))

		By("using the wildcard container policy")
		Expect(vpa.Spec.ResourcePolicy.ContainerPolicies[0].ContainerName).To(Equal("*"))

		By("never setting maxAllowed, which would risk webhook rejection")
		Expect(vpa.Spec.ResourcePolicy.ContainerPolicies[0].MaxAllowed).To(BeEmpty())

		By("recreating pods to apply the floor")
		Expect(vpa.Spec.UpdatePolicy).NotTo(BeNil())
		Expect(vpa.Spec.UpdatePolicy.UpdateMode).NotTo(BeNil())
		Expect(*vpa.Spec.UpdatePolicy.UpdateMode).To(Equal(vpav1.UpdateModeRecreate))

		By("controlling only CPU, leaving memory requests alone")
		Expect(vpa.Spec.ResourcePolicy.ContainerPolicies[0].ControlledResources).NotTo(BeNil())
		Expect(*vpa.Spec.ResourcePolicy.ContainerPolicies[0].ControlledResources).To(Equal([]corev1.ResourceName{corev1.ResourceCPU}))
	})

	It("is idempotent when nothing has changed", func() {
		createStepScaler("idempotent", "100m", "1000m")
		reconcileOnce("idempotent")

		vpaBefore := getVPA("idempotent")
		ssBefore := getStepScaler("idempotent")

		reconcileOnce("idempotent")

		vpaAfter := getVPA("idempotent")
		ssAfter := getStepScaler("idempotent")

		By("not rewriting the VPA on a steady-state pass")
		Expect(vpaAfter.ResourceVersion).To(Equal(vpaBefore.ResourceVersion))

		By("not rewriting the StepScaler's status on a steady-state pass")
		Expect(ssAfter.ResourceVersion).To(Equal(ssBefore.ResourceVersion))
	})

	It("raises minAllowed when steps are recorded", func() {
		createStepScaler("with-steps", "100m", "1000m")
		reconcileOnce("with-steps")
		Expect(minAllowedCPU(getVPA("with-steps"))).To(Equal("100m"))

		By("recording 7431 steps on status")
		// Re-Get first: the reconcile above already wrote status, so the
		// object created earlier is stale and would lose a conflict.
		ss := getStepScaler("with-steps")
		steps := int64(7431)
		now := metav1.Now()
		ss.Status.Steps = &steps
		ss.Status.StepsUpdatedAt = &now
		Expect(k8sClient.Status().Update(ctx, ss)).To(Succeed())

		By("computing 100m + 7431/10000 * 900m = 768m")
		reconcileOnce("with-steps")
		Expect(minAllowedCPU(getVPA("with-steps"))).To(Equal("768m"))

		updated := getStepScaler("with-steps")
		Expect(updated.Status.CurrentCPU).NotTo(BeNil())
		Expect(updated.Status.CurrentCPU.String()).To(Equal("768m"))
	})

	It("rejects a spec whose maxCPU is below minCPU without creating a VPA", func() {
		createStepScaler("inverted", "1000m", "100m")

		recorder := record.NewFakeRecorder(10)
		_, err := (&StepScalerReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: recorder,
		}).Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "inverted", Namespace: ns},
		})
		Expect(err).NotTo(HaveOccurred())

		By("reporting InvalidSpec on the Ready condition")
		ss := getStepScaler("inverted")
		var cond *metav1.Condition
		for i := range ss.Status.Conditions {
			if ss.Status.Conditions[i].Type == conditionReady {
				cond = &ss.Status.Conditions[i]
			}
		}
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("InvalidSpec"))
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))

		By("emitting a warning event")
		select {
		case msg := <-recorder.Events:
			Expect(msg).To(ContainSubstring("InvalidSpec"))
		default:
			Fail("expected a warning event on the recorder")
		}

		By("not creating a VPA for an invalid spec")
		vpa := &vpav1.VerticalPodAutoscaler{}
		getErr := k8sClient.Get(ctx, types.NamespacedName{Name: "inverted", Namespace: ns}, vpa)
		Expect(apierrors.IsNotFound(getErr)).To(BeTrue())
	})
})
