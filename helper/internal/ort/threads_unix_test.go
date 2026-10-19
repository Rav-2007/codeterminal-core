//go:build !windows

package ort

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// The intra-op thread count must reach ONNX Runtime, and nothing about the
// vectors shows whether it did: on a 13th-gen Intel i5 one thread and the
// default (one per core) produce the same bits for every golden text. What
// does show it is the CPU an inference burns per second of wall time. Measured
// 2026-10-19 on that machine, one 512-token text: one thread 1.00, two threads
// 2.00, the default 11.85. So one thread must stay near 1 and two must clearly
// exceed it.
//
// Unix-only because it reads the process's CPU time with getrusage; the
// session options call it checks is the same code on every platform.
func TestTheThreadCountReachesONNXRuntime(t *testing.T) {
	initialized(t)
	if runtime.NumCPU() < 2 {
		t.Skip("needs two CPUs to tell one thread from two")
	}
	home, _ := os.UserHomeDir()
	model := filepath.Join(home, ".mochiii", "models", "bge-small-en-v1.5-int8", "model_int8.onnx")
	if _, err := os.Stat(model); err != nil {
		t.Skipf("model not cached: %v", err)
	}
	const n = 512
	ids, mask, types := make([]int64, n), make([]int64, n), make([]int64, n)
	for i := range ids {
		ids[i] = int64(1000 + i%2000)
		mask[i] = 1
	}
	ratio := func(threads int) float64 {
		s, err := NewSession(model, []string{"input_ids", "attention_mask", "token_type_ids"}, []string{"last_hidden_state"}, threads)
		if err != nil {
			t.Fatalf("opening the model with %d threads: %v", threads, err)
		}
		defer func() { _ = s.Destroy() }()
		if _, err := s.Run([]int64{1, n}, ids, mask, types); err != nil {
			t.Fatalf("warm-up run: %v", err)
		}
		c0, w0 := cpuTime(t), time.Now()
		for i := 0; i < 4; i++ {
			if _, err := s.Run([]int64{1, n}, ids, mask, types); err != nil {
				t.Fatalf("run: %v", err)
			}
		}
		return float64(cpuTime(t)-c0) / float64(time.Since(w0))
	}
	one, two := ratio(1), ratio(2)
	t.Logf("CPU per wall second: one thread %.2f, two threads %.2f", one, two)
	if one > 1.4 {
		t.Errorf("one intra-op thread used %.2f CPUs: the thread count did not reach ONNX Runtime", one)
	}
	if two < one+0.4 {
		t.Errorf("two intra-op threads used %.2f CPUs against one thread's %.2f: the thread count did not reach ONNX Runtime", two, one)
	}
}

func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
