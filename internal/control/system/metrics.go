// Package system measures the host without reading sandbox-owned files.
package system

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Capacity struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	Available uint64 `json:"available"`
}

type Snapshot struct {
	CPU       *float64  `json:"cpu"`
	Memory    *Capacity `json:"memory"`
	Disk      *Capacity `json:"disk"`
	SampledAt time.Time `json:"sampled_at"`
	Errors    []string  `json:"errors,omitempty"`
}

type cpuTime struct{ total, idle uint64 }

type Sampler struct {
	mu       sync.Mutex
	path     string
	previous cpuTime
	sampled  bool
	last     Snapshot
}

func New(dataDir string) *Sampler { return &Sampler{path: dataDir} }

func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.last.SampledAt) < time.Second {
		return s.last
	}
	r := Snapshot{SampledAt: time.Now().UTC()}
	f, err := os.Open("/proc/stat")
	if err == nil {
		var cur cpuTime
		cur, err = readCPU(f)
		f.Close()
		if err == nil {
			if s.sampled {
				r.CPU = cpuPercent(s.previous, cur)
			}
			s.previous, s.sampled = cur, true
		}
	}
	if err != nil {
		r.Errors = append(r.Errors, "CPU usage unavailable")
	}
	f, err = os.Open("/proc/meminfo")
	if err == nil {
		var m Capacity
		m, err = readMemory(f)
		f.Close()
		if err == nil {
			r.Memory = &m
		}
	}
	if err != nil {
		r.Errors = append(r.Errors, "Memory usage unavailable")
	}
	d, err := diskCapacity(s.path)
	if err == nil {
		r.Disk = &d
	} else {
		r.Errors = append(r.Errors, "Disk usage unavailable")
	}
	s.last = r
	return r
}

func readCPU(r io.Reader) (cpuTime, error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		return cpuTime{}, errors.New("missing CPU counters")
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 9 || fields[0] != "cpu" {
		return cpuTime{}, errors.New("invalid CPU counters")
	}
	var c cpuTime
	// Guest counters are already included in user and nice time.
	for i, v := range fields[1:9] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return c, err
		}
		c.total += n
		if i == 3 || i == 4 {
			c.idle += n
		}
	}
	return c, nil
}

func cpuPercent(a, b cpuTime) *float64 {
	if b.total <= a.total || b.idle < a.idle {
		return nil
	}
	total, idle := b.total-a.total, b.idle-a.idle
	if idle > total {
		return nil
	}
	p := 100 * float64(total-idle) / float64(total)
	return &p
}

func readMemory(r io.Reader) (Capacity, error) {
	var c Capacity
	found := 0
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || (f[0] != "MemTotal:" && f[0] != "MemAvailable:") {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return c, err
		}
		if f[0] == "MemTotal:" {
			c.Total = n * 1024
			found |= 1
		} else {
			c.Available = n * 1024
			found |= 2
		}
	}
	if err := sc.Err(); err != nil {
		return c, err
	}
	if found != 3 || c.Total == 0 || c.Available > c.Total {
		return c, fmt.Errorf("invalid memory counters")
	}
	c.Used = c.Total - c.Available
	return c, nil
}
