package cpu

import (
	"errors"
	"testing"
)

func TestTargetMilliCPU(t *testing.T) {
	tests := []struct {
		name     string
		steps    int64
		goal     int64
		minMilli int64
		maxMilli int64
		want     int64
	}{
		{"no steps yields the floor", 0, 10000, 100, 1000, 100},
		{"spec worked example", 7431, 10000, 100, 1000, 768},
		{"exactly at goal yields the ceiling", 10000, 10000, 100, 1000, 1000},
		{"above goal clamps to the ceiling", 25000, 10000, 100, 1000, 1000},
		{"negative steps clamp to the floor", -5, 10000, 100, 1000, 100},
		{"min equals max is constant", 5000, 10000, 500, 500, 500},
		{"integer division truncates, never rounds up", 1, 10000, 100, 1000, 100},
		{"half a goal is half the range", 5000, 10000, 100, 1000, 550},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TargetMilliCPU(tt.steps, tt.goal, tt.minMilli, tt.maxMilli)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("TargetMilliCPU(%d, %d, %d, %d) = %d, want %d",
					tt.steps, tt.goal, tt.minMilli, tt.maxMilli, got, tt.want)
			}
		})
	}
}

func TestTargetMilliCPUValidation(t *testing.T) {
	tests := []struct {
		name     string
		goal     int64
		minMilli int64
		maxMilli int64
		wantErr  error
	}{
		{"zero goal would divide by zero", 0, 100, 1000, ErrGoalTooSmall},
		{"negative goal", -1, 100, 1000, ErrGoalTooSmall},
		{"zero min is silently ignored by VPA", 10000, 0, 1000, ErrMinCPUNotPositive},
		{"negative min", 10000, -5, 1000, ErrMinCPUNotPositive},
		{"max below min inverts the slope", 10000, 1000, 100, ErrMaxBelowMin},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TargetMilliCPU(5000, tt.goal, tt.minMilli, tt.maxMilli)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("got error %v, want %v", err, tt.wantErr)
			}
		})
	}
}
