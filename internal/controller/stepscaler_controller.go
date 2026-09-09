package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	getoffyoursaasiov1alpha1 "github.com/circa10a/getoffyoursaas/api/v1alpha1"
	"github.com/circa10a/getoffyoursaas/internal/cpu"
)

// conditionReady is the single condition type this controller reports on.
const conditionReady = "Ready"

// StepScalerReconciler reconciles a StepScaler object
type StepScalerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=getoffyoursaas.io,resources=stepscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=getoffyoursaas.io,resources=stepscalers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=getoffyoursaas.io,resources=stepscalers/finalizers,verbs=update
// +kubebuilder:rbac:groups=autoscaling.k8s.io,resources=verticalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.24.1/pkg/reconcile
func (r *StepScalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var ss getoffyoursaasiov1alpha1.StepScaler
	if err := r.Get(ctx, req.NamespacedName, &ss); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var steps int64
	if ss.Status.Steps != nil {
		steps = *ss.Status.Steps
	}

	targetMilli, err := cpu.TargetMilliCPU(
		steps, ss.Spec.DailyStepGoal,
		ss.Spec.MinCPU.MilliValue(), ss.Spec.MaxCPU.MilliValue(),
	)
	if err != nil {
		r.Recorder.Event(&ss, corev1.EventTypeWarning, "InvalidSpec", err.Error())
		meta.SetStatusCondition(&ss.Status.Conditions, metav1.Condition{
			Type:    conditionReady,
			Status:  metav1.ConditionFalse,
			Reason:  "InvalidSpec",
			Message: err.Error(),
		})
		// Spec errors are not retryable; wait for the user to fix the CR.
		return ctrl.Result{}, r.Status().Update(ctx, &ss)
	}

	desired := resource.NewMilliQuantity(targetMilli, resource.DecimalSI)

	containerName := ss.Spec.ContainerName
	if containerName == "" {
		containerName = "*"
	}

	vpa := &vpav1.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: ss.Name, Namespace: ss.Namespace},
	}
	updateMode := vpav1.UpdateModeRecreate
	controlled := []corev1.ResourceName{corev1.ResourceCPU}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, vpa, func() error {
		vpa.Spec.TargetRef = ss.Spec.ScaleTargetRef.DeepCopy()
		vpa.Spec.UpdatePolicy = &vpav1.PodUpdatePolicy{UpdateMode: &updateMode}
		vpa.Spec.ResourcePolicy = &vpav1.PodResourcePolicy{
			ContainerPolicies: []vpav1.ContainerResourcePolicy{{
				ContainerName:       containerName,
				ControlledResources: &controlled,
				MinAllowed:          corev1.ResourceList{corev1.ResourceCPU: *desired},
			}},
		}
		return controllerutil.SetControllerReference(&ss, vpa, r.Scheme)
	})
	if err != nil {
		// No special case for a missing VPA CRD: Owns() below makes the manager
		// fail its cache sync and exit before Reconcile ever runs, so this code
		// could never report it. VPA is a hard prerequisite, documented as such.
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		log.Info("reconciled VPA", "operation", op, "minAllowedCPU", desired.String())
	}

	// Guard the status write. Our own status updates come back as watch events,
	// so writing unconditionally on every pass risks a self-sustaining reconcile
	// loop. Only write when something actually changed.
	before := ss.Status.DeepCopy()
	ss.Status.CurrentCPU = desired
	meta.SetStatusCondition(&ss.Status.Conditions, metav1.Condition{
		Type:    conditionReady,
		Status:  metav1.ConditionTrue,
		Reason:  "Reconciled",
		Message: fmt.Sprintf("%d steps earned a %s CPU floor", steps, desired.String()),
	})
	if equality.Semantic.DeepEqual(before, &ss.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Status().Update(ctx, &ss)
}

// SetupWithManager sets up the controller with the Manager.
func (r *StepScalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&getoffyoursaasiov1alpha1.StepScaler{}).
		Owns(&vpav1.VerticalPodAutoscaler{}).
		Named("stepscaler").
		Complete(r)
}
