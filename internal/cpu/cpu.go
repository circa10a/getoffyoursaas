// Package cpu converts step counts into CPU floors.
package cpu

import "errors"

var (
	ErrGoalTooSmall      = errors.New("dailyStepGoal must be at least 1")
	ErrMinCPUNotPositive = errors.New("minCPU must be greater than zero")
	ErrMaxBelowMin       = errors.New("maxCPU must be greater than or equal to minCPU")
)

// TargetMilliCPU returns the CPU floor in milliCPU earned by the given step
// count: minMilli at zero steps, rising linearly to maxMilli at dailyStepGoal.
//
// A zero minMilli is rejected rather than clamped because VPA silently discards
// a minAllowed of zero, which would make the whole controller a no-op.
func TargetMilliCPU(steps, dailyStepGoal, minMilli, maxMilli int64) (int64, error) {
	if dailyStepGoal < 1 {
		return 0, ErrGoalTooSmall
	}
	if minMilli <= 0 {
		return 0, ErrMinCPUNotPositive
	}
	if maxMilli < minMilli {
		return 0, ErrMaxBelowMin
	}

	if steps < 0 {
		steps = 0
	}
	if steps > dailyStepGoal {
		steps = dailyStepGoal
	}

	return minMilli + (steps*(maxMilli-minMilli))/dailyStepGoal, nil
}
