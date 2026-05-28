/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector/ai/gpu"
)

func TestParseDeviceList(t *testing.T) {
	cases := []struct {
		in     string
		want   []int
		errSub string
	}{
		{in: "", want: []int{0}},
		{in: "0", want: []int{0}},
		{in: "0,1,2", want: []int{0, 1, 2}},
		{in: " 1 , 1 , 2 ", want: []int{1, 2}},
		{in: "a", errSub: "integer"},
		{in: "-1", errSub: ">= 0"},
	}
	for _, c := range cases {
		got, err := parseDeviceList(c.in)
		if c.errSub != "" {
			if err == nil || !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("in=%q want err containing %q, got %v", c.in, c.errSub, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("in=%q: %v", c.in, err)
			continue
		}
		if !intSliceEq(got, c.want) {
			t.Errorf("in=%q got %v want %v", c.in, got, c.want)
		}
	}
}

func TestGPUMemInjectorValidator(t *testing.T) {
	i := &GPUMemInjector{}
	i.SetDefault()
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("zero size should fail validation")
	}
	i.Args.SizeMiB = 100
	i.Args.Devices = "0,foo"
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("bad devices list should fail validation")
	}
	i.Args.Devices = "0,1"
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("valid args should pass: %v", err)
	}
}

func TestGPUMemInjectorLifecycleFake(t *testing.T) {
	fake := gpu.NewFakeDriver(4, 0)
	gpu.Register(fake)
	defer gpu.Reset()

	i := &GPUMemInjector{}
	i.SetDefault()
	i.Args = GPUMemArgs{SizeMiB: 256, Devices: "0,2"}
	ctx := context.Background()
	if err := i.Validator(ctx); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(ctx); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if got := fake.UsedBytes(0); got != 256*1024*1024 {
		t.Errorf("dev0 used=%d", got)
	}
	if got := fake.UsedBytes(2); got != 256*1024*1024 {
		t.Errorf("dev2 used=%d", got)
	}
	if len(i.Runtime.Handles) != 2 {
		t.Fatalf("runtime handles=%d, want 2", len(i.Runtime.Handles))
	}
	if err := i.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if fake.Outstanding() != 0 {
		t.Fatalf("outstanding=%d after recover, want 0", fake.Outstanding())
	}
}

func TestGPUMemInjectorRollbackOnFailure(t *testing.T) {
	// Per-device limit forces the second device to fail.
	fake := gpu.NewFakeDriver(2, 100*1024*1024)
	gpu.Register(fake)
	defer gpu.Reset()

	i := &GPUMemInjector{}
	i.SetDefault()
	i.Args = GPUMemArgs{SizeMiB: 200, Devices: "0,1"}
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err == nil {
		t.Fatal("Inject should fail with size > limit")
	}
	// Even on failure, no leak from the first device.
	if fake.Outstanding() != 0 {
		t.Fatalf("outstanding=%d, want 0 (rollback)", fake.Outstanding())
	}
}

func TestGPUMemInjectorNoDriver(t *testing.T) {
	gpu.Reset()
	i := &GPUMemInjector{}
	i.SetDefault()
	i.Args = GPUMemArgs{SizeMiB: 64}
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err == nil {
		t.Fatal("null driver should make Inject fail")
	}
}

func intSliceEq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
