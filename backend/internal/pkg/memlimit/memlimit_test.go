package memlimit

import (
	"math"
	"testing"
)

func TestCalculateRuntimeMemoryLimit(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want int64
	}{
		{name: "two gib", in: 2 << 30, want: 1536 << 20},
		{name: "one gib", in: 1 << 30, want: 768 << 20},
		{name: "zero", in: 0, want: 0},
		{name: "unlimited", in: math.MaxInt64, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CalculateRuntimeMemoryLimit(tt.in); got != tt.want {
				t.Fatalf("CalculateRuntimeMemoryLimit(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestCgroupSubsystemsContain(t *testing.T) {
	if !cgroupSubsystemsContain("cpu,memory,pids", "memory") {
		t.Fatalf("expected memory subsystem to match")
	}
	if cgroupSubsystemsContain("cpuacct", "memory") {
		t.Fatalf("unexpected subsystem match")
	}
}
