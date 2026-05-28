/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

// Package gpu wraps GPU memory allocation behind a small interface so AI
// injectors can be exercised on a developer laptop without CUDA/NVML present.
// On a real node a CGo build can register a driver backed by NVML; everywhere
// else the null driver is used and the call returns a clear error.
package gpu

import (
	"errors"
	"fmt"
	"sync"
)

// ErrNoDriver is returned when no real GPU driver has been registered. It is
// the explicit signal a caller can match on to fall back / report to the user.
var ErrNoDriver = errors.New("no gpu driver registered (running without NVML/CUDA)")

// ErrUnknownHandle is returned by Free for handles the driver does not know
// about. Drivers must never return this on a handle they previously returned
// from Allocate.
var ErrUnknownHandle = errors.New("unknown gpu allocation handle")

// Handle uniquely identifies an allocation owned by a Driver. It is opaque to
// callers; injectors persist it to runtime state and pass it back to Free
// during recovery.
type Handle string

// Driver is the minimal contract an AI injector needs from a GPU. Returning
// an error from Allocate must leave no allocation on the device; Free must be
// idempotent for already-freed handles.
type Driver interface {
	Name() string
	DeviceCount() int
	Allocate(device int, sizeBytes uint64) (Handle, error)
	Free(handle Handle) error
}

var (
	mu      sync.RWMutex
	current Driver = nullDriver{}
)

// Register installs the active driver. The last call wins; intended to be
// called from package init() in a build-tagged file that wraps NVML.
func Register(d Driver) {
	if d == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	current = d
}

// Active returns the currently registered driver.
func Active() Driver {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Reset restores the null driver. Intended for tests.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	current = nullDriver{}
}

type nullDriver struct{}

func (nullDriver) Name() string                          { return "null" }
func (nullDriver) DeviceCount() int                      { return 0 }
func (nullDriver) Allocate(int, uint64) (Handle, error)  { return "", ErrNoDriver }
func (nullDriver) Free(Handle) error                     { return ErrNoDriver }

// FakeDriver is a deterministic in-memory Driver used by injector tests.
// It records every allocation so tests can assert what the injector did
// without needing a real GPU.
type FakeDriver struct {
	mu      sync.Mutex
	devices int
	limit   uint64
	used    map[int]uint64
	allocs  map[Handle]fakeAlloc
	next    uint64
	freed   map[Handle]bool
}

type fakeAlloc struct {
	Device int
	Size   uint64
}

// NewFakeDriver returns a fake with the requested device count and a per-device
// byte limit. A limit of 0 disables the limit.
func NewFakeDriver(devices int, perDeviceLimit uint64) *FakeDriver {
	return &FakeDriver{
		devices: devices,
		limit:   perDeviceLimit,
		used:    make(map[int]uint64),
		allocs:  make(map[Handle]fakeAlloc),
		freed:   make(map[Handle]bool),
	}
}

func (f *FakeDriver) Name() string     { return "fake" }
func (f *FakeDriver) DeviceCount() int { return f.devices }

func (f *FakeDriver) Allocate(device int, size uint64) (Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if device < 0 || device >= f.devices {
		return "", fmt.Errorf("device %d out of range [0,%d)", device, f.devices)
	}
	if size == 0 {
		return "", fmt.Errorf("size must be > 0")
	}
	if f.limit > 0 && f.used[device]+size > f.limit {
		return "", fmt.Errorf("device %d out of memory: used=%d req=%d limit=%d",
			device, f.used[device], size, f.limit)
	}
	f.next++
	h := Handle(fmt.Sprintf("fake-%d-%d", device, f.next))
	f.used[device] += size
	f.allocs[h] = fakeAlloc{Device: device, Size: size}
	return h, nil
}

func (f *FakeDriver) Free(h Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.allocs[h]
	if !ok {
		if f.freed[h] {
			return nil // idempotent
		}
		return ErrUnknownHandle
	}
	f.used[a.Device] -= a.Size
	delete(f.allocs, h)
	f.freed[h] = true
	return nil
}

// Outstanding returns the number of unfreed handles. Useful for tests.
func (f *FakeDriver) Outstanding() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.allocs)
}

// UsedBytes returns used bytes on a device. Useful for tests.
func (f *FakeDriver) UsedBytes(device int) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.used[device]
}
