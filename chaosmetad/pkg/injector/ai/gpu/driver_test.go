/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package gpu

import (
	"errors"
	"testing"
)

func TestNullDriver(t *testing.T) {
	Reset()
	d := Active()
	if d.Name() != "null" {
		t.Fatalf("expected null driver, got %q", d.Name())
	}
	if _, err := d.Allocate(0, 1024); !errors.Is(err, ErrNoDriver) {
		t.Fatalf("expected ErrNoDriver, got %v", err)
	}
	if err := d.Free("anything"); !errors.Is(err, ErrNoDriver) {
		t.Fatalf("expected ErrNoDriver on Free, got %v", err)
	}
}

func TestRegisterAndReset(t *testing.T) {
	f := NewFakeDriver(2, 0)
	Register(f)
	if Active().Name() != "fake" {
		t.Fatalf("Register did not install driver")
	}
	Reset()
	if Active().Name() != "null" {
		t.Fatalf("Reset did not restore null driver")
	}
}

func TestFakeDriverAllocateFree(t *testing.T) {
	f := NewFakeDriver(2, 10*1024*1024)
	h1, err := f.Allocate(0, 4*1024*1024)
	if err != nil {
		t.Fatalf("alloc 4MiB: %v", err)
	}
	h2, err := f.Allocate(1, 8*1024*1024)
	if err != nil {
		t.Fatalf("alloc 8MiB on dev 1: %v", err)
	}
	if f.Outstanding() != 2 {
		t.Fatalf("outstanding=%d, want 2", f.Outstanding())
	}
	if got := f.UsedBytes(0); got != 4*1024*1024 {
		t.Fatalf("dev0 used=%d", got)
	}
	if err := f.Free(h1); err != nil {
		t.Fatalf("free h1: %v", err)
	}
	if err := f.Free(h2); err != nil {
		t.Fatalf("free h2: %v", err)
	}
	if f.Outstanding() != 0 {
		t.Fatalf("outstanding=%d after free, want 0", f.Outstanding())
	}
	// double free is idempotent
	if err := f.Free(h1); err != nil {
		t.Fatalf("double free should be idempotent: %v", err)
	}
}

func TestFakeDriverRejectsBadInput(t *testing.T) {
	f := NewFakeDriver(1, 0)
	if _, err := f.Allocate(-1, 100); err == nil {
		t.Fatal("expected error on negative device")
	}
	if _, err := f.Allocate(0, 0); err == nil {
		t.Fatal("expected error on zero size")
	}
	if err := f.Free("nonexistent"); !errors.Is(err, ErrUnknownHandle) {
		t.Fatalf("expected ErrUnknownHandle, got %v", err)
	}
}

func TestFakeDriverPerDeviceLimit(t *testing.T) {
	f := NewFakeDriver(1, 1024)
	if _, err := f.Allocate(0, 512); err != nil {
		t.Fatalf("alloc 512: %v", err)
	}
	if _, err := f.Allocate(0, 1024); err == nil {
		t.Fatal("expected OOM on second alloc exceeding limit")
	}
	// After OOM, freeing the first allocation makes room.
	for _, h := range outstandingHandles(f) {
		_ = f.Free(h)
	}
	if _, err := f.Allocate(0, 1024); err != nil {
		t.Fatalf("alloc after free: %v", err)
	}
}

func outstandingHandles(f *FakeDriver) []Handle {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Handle, 0, len(f.allocs))
	for h := range f.allocs {
		out = append(out, h)
	}
	return out
}
