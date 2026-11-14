package system

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCPUIntervals(t *testing.T) {
	a, err := readCPU(strings.NewReader("cpu 100 20 30 400 50 6 7 8 90 0\ncpu0 1 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := readCPU(strings.NewReader("cpu 120 20 40 460 60 6 7 8 90 0\n"))
	if got := cpuPercent(a, b); got == nil || *got != 30 {
		t.Fatalf("CPU = %v, want 30; guest time must not count twice", got)
	}
	if got := cpuPercent(b, a); got != nil {
		t.Fatalf("reset counters = %v", got)
	}
}

func TestMemoryAvailable(t *testing.T) {
	m, err := readMemory(strings.NewReader("MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 400 kB\nCached: 300 kB\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Total != 1024000 || m.Used != 614400 || m.Available != 409600 {
		t.Fatalf("memory: %+v", m)
	}
	if _, err := readMemory(strings.NewReader("MemTotal: 1 kB\n")); err == nil {
		t.Fatal("missing available memory accepted")
	}
}

func TestSnapshot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host metrics require Linux")
	}
	s := New(t.TempDir())
	r := s.Snapshot()
	if r.Memory == nil || r.Disk == nil || r.Disk.Total == 0 || r.SampledAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("snapshot: %+v", r)
	}
	if r.CPU != nil {
		t.Fatal("first sample should be warming up")
	}
}
