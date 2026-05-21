package memlimit

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
)

const defaultAutoMemoryLimitRatio = 0.75

type AutoMemoryLimitResult struct {
	Applied      bool
	Skipped      bool
	Reason       string
	CgroupLimit  int64
	RuntimeLimit int64
}

func ApplyAutoMemoryLimit() AutoMemoryLimitResult {
	if strings.TrimSpace(os.Getenv("GOMEMLIMIT")) != "" {
		return AutoMemoryLimitResult{Skipped: true, Reason: "GOMEMLIMIT already set"}
	}

	cgroupLimit, err := DetectCgroupMemoryLimit()
	if err != nil {
		return AutoMemoryLimitResult{Skipped: true, Reason: err.Error()}
	}
	runtimeLimit := CalculateRuntimeMemoryLimit(cgroupLimit)
	if runtimeLimit <= 0 {
		return AutoMemoryLimitResult{Skipped: true, Reason: "cgroup memory limit is too small", CgroupLimit: cgroupLimit}
	}

	debug.SetMemoryLimit(runtimeLimit)
	return AutoMemoryLimitResult{
		Applied:      true,
		CgroupLimit:  cgroupLimit,
		RuntimeLimit: runtimeLimit,
	}
}

func CalculateRuntimeMemoryLimit(cgroupLimit int64) int64 {
	if cgroupLimit <= 0 || cgroupLimit == math.MaxInt64 {
		return 0
	}
	return int64(float64(cgroupLimit) * defaultAutoMemoryLimitRatio)
}

func DetectCgroupMemoryLimit() (int64, error) {
	if limit, err := readCgroupMemoryLimitFile("/sys/fs/cgroup/memory.max"); err == nil {
		return limit, nil
	}

	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0, fmt.Errorf("read cgroup metadata: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 3 {
			continue
		}
		if fields[0] == "0" && fields[1] == "" {
			path := filepath.Clean("/" + strings.TrimPrefix(fields[2], "/"))
			return readCgroupMemoryLimitFile(filepath.Join("/sys/fs/cgroup", path, "memory.max"))
		}
		if cgroupSubsystemsContain(fields[1], "memory") {
			path := filepath.Clean("/" + strings.TrimPrefix(fields[2], "/"))
			return readCgroupMemoryLimitFile(filepath.Join("/sys/fs/cgroup/memory", path, "memory.limit_in_bytes"))
		}
	}
	return 0, errors.New("cgroup memory limit not found")
}

func cgroupSubsystemsContain(raw string, target string) bool {
	for _, part := range strings.Split(raw, ",") {
		if part == target {
			return true
		}
	}
	return false
}

func readCgroupMemoryLimitFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "max" {
		return 0, errors.New("cgroup memory limit is unlimited")
	}
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse cgroup memory limit %q: %w", raw, err)
	}
	if limit <= 0 || limit >= math.MaxInt64/2 {
		return 0, errors.New("cgroup memory limit is unlimited")
	}
	return limit, nil
}
