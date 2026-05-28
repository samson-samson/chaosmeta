/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

// Package procmem abstracts the bits of process discovery and memory pressure
// the AI KV-cache injector needs, so the injector can be tested without
// scanning /proc on the host.
package procmem

import (
	"errors"
	"fmt"
	"sync"
)

// Process describes a single inference worker for the injector.
type Process struct {
	PID  int
	Name string // argv[0] basename or comm
	Cmd  string // full cmdline, for matching
}

// Manager is the surface the kvcache_pressure injector relies on.
//   - Find returns processes whose Cmd matches the pattern (substring match
//     is fine; the real /proc-backed implementation can do regex).
//   - SetOomScoreAdj writes /proc/<pid>/oom_score_adj. Returning the previous
//     value lets the injector restore it on Recover.
//   - AllocateMemory pins `bytes` of resident memory; the returned handle is
//     passed to FreeMemory on Recover.
type Manager interface {
	Find(pattern string) ([]Process, error)
	SetOomScoreAdj(pid int, score int) (previous int, err error)
	AllocateMemory(bytes uint64) (Handle, error)
	FreeMemory(h Handle) error
}

// Handle identifies a pressure allocation.
type Handle string

// ErrNotFound means a PID was unknown to the manager (e.g., process already
// exited between Find and SetOomScoreAdj).
var ErrNotFound = errors.New("process not found")

// FakeManager is a deterministic test double.
type FakeManager struct {
	mu          sync.Mutex
	Processes   []Process
	oomBefore   map[int]int
	OomCurrent  map[int]int
	allocs      map[Handle]uint64
	nextHandle  uint64
	allocTotal  uint64
	FailOnAlloc bool
}

// NewFakeManager seeds the manager with a list of processes.
func NewFakeManager(procs ...Process) *FakeManager {
	return &FakeManager{
		Processes:  append([]Process(nil), procs...),
		oomBefore:  make(map[int]int),
		OomCurrent: make(map[int]int),
		allocs:     make(map[Handle]uint64),
	}
}

func (f *FakeManager) Find(pattern string) ([]Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Process
	for _, p := range f.Processes {
		if pattern == "" || containsAny(p.Cmd, pattern) || containsAny(p.Name, pattern) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *FakeManager) SetOomScoreAdj(pid int, score int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.Processes {
		if p.PID == pid {
			prev, seen := f.OomCurrent[pid]
			if !seen {
				prev = 0
				f.oomBefore[pid] = 0
			}
			f.OomCurrent[pid] = score
			return prev, nil
		}
	}
	return 0, ErrNotFound
}

func (f *FakeManager) AllocateMemory(bytes uint64) (Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailOnAlloc {
		return "", fmt.Errorf("simulated allocation failure")
	}
	if bytes == 0 {
		return "", fmt.Errorf("bytes must be > 0")
	}
	f.nextHandle++
	h := Handle(fmt.Sprintf("alloc-%d", f.nextHandle))
	f.allocs[h] = bytes
	f.allocTotal += bytes
	return h, nil
}

func (f *FakeManager) FreeMemory(h Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.allocs[h]
	if !ok {
		return nil // idempotent
	}
	f.allocTotal -= b
	delete(f.allocs, h)
	return nil
}

// TotalAllocated returns the sum of outstanding allocations. For tests.
func (f *FakeManager) TotalAllocated() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allocTotal
}

// OutstandingAllocs returns the number of unfreed allocations. For tests.
func (f *FakeManager) OutstandingAllocs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.allocs)
}

func containsAny(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

var (
	mu      sync.RWMutex
	current Manager = nilManager{}
)

// Register installs the active manager. Build-tag scoped files can register a
// /proc-backed manager on linux; tests register a FakeManager.
func Register(m Manager) {
	if m == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	current = m
}

// Active returns the registered Manager.
func Active() Manager {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Reset restores the nil manager. For tests.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	current = nilManager{}
}

type nilManager struct{}

func (nilManager) Find(string) ([]Process, error)         { return nil, fmt.Errorf("no procmem manager registered") }
func (nilManager) SetOomScoreAdj(int, int) (int, error)   { return 0, fmt.Errorf("no procmem manager registered") }
func (nilManager) AllocateMemory(uint64) (Handle, error)  { return "", fmt.Errorf("no procmem manager registered") }
func (nilManager) FreeMemory(Handle) error                { return fmt.Errorf("no procmem manager registered") }
