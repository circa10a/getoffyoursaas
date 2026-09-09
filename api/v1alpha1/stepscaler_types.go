package v1alpha1

import (
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// StepScalerSpec defines the desired state of StepScaler.
type StepScalerSpec struct {
	// ScaleTargetRef identifies the workload whose CPU floor is driven by step
	// count. Same shape HPA and VPA use.
	ScaleTargetRef autoscalingv1.CrossVersionObjectReference `json:"scaleTargetRef"`

	// ContainerName selects which container the policy applies to. The default
	// "*" is VPA's wildcard, applying to every container that has no policy of
	// its own. An empty string is rejected by VPA's webhook, so it is defaulted
	// rather than left blank.
	// +kubebuilder:default="*"
	// +optional
	ContainerName string `json:"containerName,omitempty"`

	// DailyStepGoal is the step count that earns MaxCPU.
	// +kubebuilder:validation:Minimum=1
	DailyStepGoal int64 `json:"dailyStepGoal"`

	// MinCPU is the floor at zero steps. Must be greater than zero: VPA
	// silently ignores a minAllowed of zero.
	MinCPU resource.Quantity `json:"minCPU"`

	// MaxCPU is the floor at or above DailyStepGoal. Must be >= MinCPU: a smaller
	// MaxCPU inverts the slope, so walking would *reduce* the CPU floor.
	MaxCPU resource.Quantity `json:"maxCPU"`
}

// StepScalerStatus defines the observed state of StepScaler.
type StepScalerStatus struct {
	// Steps is the most recent reading from the ingest endpoint.
	// +optional
	Steps *int64 `json:"steps,omitempty"`

	// StepsUpdatedAt is when that reading arrived.
	// +optional
	StepsUpdatedAt *metav1.Time `json:"stepsUpdatedAt,omitempty"`

	// CurrentCPU is the floor most recently written to the owned VPA.
	// +optional
	CurrentCPU *resource.Quantity `json:"currentCPU,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.scaleTargetRef.name`
// +kubebuilder:printcolumn:name="Steps",type=integer,JSONPath=`.status.steps`
// +kubebuilder:printcolumn:name="CPU",type=string,JSONPath=`.status.currentCPU`
// +kubebuilder:printcolumn:name="Goal",type=integer,JSONPath=`.spec.dailyStepGoal`

// StepScaler is the Schema for the stepscalers API
type StepScaler struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of StepScaler
	// +required
	Spec StepScalerSpec `json:"spec"`

	// status defines the observed state of StepScaler
	// +optional
	Status StepScalerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// StepScalerList contains a list of StepScaler
type StepScalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []StepScaler `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &StepScaler{}, &StepScalerList{})
		return nil
	})
}
