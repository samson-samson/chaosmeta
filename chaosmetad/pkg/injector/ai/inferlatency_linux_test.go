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
	"fmt"
	"sync"
	"testing"
)

// fakeStarter records what the injector tried to start, without spawning.
type fakeStarter struct {
	mu       sync.Mutex
	StartArr []startCall
	StopArr  []int
	FailNext bool
	NextPID  int
}

type startCall struct {
	Name string
	Args []string
}

func (f *fakeStarter) Start(name string, args []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.StartArr = append(f.StartArr, startCall{Name: name, Args: append([]string(nil), args...)})
	if f.FailNext {
		f.FailNext = false
		return 0, fmt.Errorf("simulated start failure")
	}
	if f.NextPID == 0 {
		f.NextPID = 12000
	}
	pid := f.NextPID
	f.NextPID++
	return pid, nil
}

func (f *fakeStarter) Stop(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.StopArr = append(f.StopArr, pid)
	return nil
}

func TestInferLatencyValidator(t *testing.T) {
	i := &InferLatencyInjector{}
	i.SetDefault()
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("empty args should fail validation")
	}
	i.Args = InferLatencyArgs{ListenAddr: "0.0.0.0:18080", Upstream: "http://127.0.0.1:8000"}
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("missing latency should fail")
	}
	i.Args.Latency = "100xyz"
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("invalid latency should fail")
	}
	i.Args.Latency = "100ms"
	i.Args.Jitter = "garbage"
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("invalid jitter should fail")
	}
	i.Args.Jitter = "10ms"
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("valid args should pass: %v", err)
	}
}

func TestInferLatencyInjectRecover(t *testing.T) {
	f := &fakeStarter{NextPID: 4242}
	restore := withTestStarter(f)
	defer restore()

	i := &InferLatencyInjector{}
	i.SetDefault()
	i.Args = InferLatencyArgs{
		ListenAddr: "0.0.0.0:18080",
		Upstream:   "http://127.0.0.1:8000",
		Latency:    "200ms",
		Jitter:     "50ms",
		PathPrefix: "/v1/",
	}
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if i.Runtime.PID != 4242 {
		t.Fatalf("runtime pid=%d, want 4242", i.Runtime.PID)
	}
	if len(f.StartArr) != 1 {
		t.Fatalf("want 1 start call, got %d", len(f.StartArr))
	}
	call := f.StartArr[0]
	if call.Name != FaultInferLatency {
		t.Errorf("name=%q want %q", call.Name, FaultInferLatency)
	}
	if !argsContain(call.Args, "--latency", "200ms") {
		t.Errorf("latency not in args: %v", call.Args)
	}
	if !argsContain(call.Args, "--path-prefix", "/v1/") {
		t.Errorf("path-prefix not in args: %v", call.Args)
	}
	if err := i.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(f.StopArr) != 1 || f.StopArr[0] != 4242 {
		t.Errorf("Stop calls=%v, want [4242]", f.StopArr)
	}
	if i.Runtime.PID != 0 {
		t.Errorf("runtime pid should be cleared, got %d", i.Runtime.PID)
	}
	// Recover when already recovered is a no-op.
	if err := i.Recover(context.Background()); err != nil {
		t.Errorf("second Recover: %v", err)
	}
}

func TestInferLatencyStartFailure(t *testing.T) {
	f := &fakeStarter{FailNext: true}
	restore := withTestStarter(f)
	defer restore()
	i := &InferLatencyInjector{}
	i.SetDefault()
	i.Args = InferLatencyArgs{ListenAddr: ":1", Upstream: "http://x", Latency: "1ms"}
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err == nil {
		t.Fatal("Inject should fail when starter fails")
	}
	if i.Runtime.PID != 0 {
		t.Errorf("PID should remain 0 on failure, got %d", i.Runtime.PID)
	}
}

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in     string
		ok     bool
		host   string
		port   int
	}{
		{"0.0.0.0:8080", true, "0.0.0.0", 8080},
		{":18080", true, "", 18080},
		{"8080", false, "", 0},
		{"host:abc", false, "", 0},
		{":0", false, "", 0},
		{":70000", false, "", 0},
	}
	for _, c := range cases {
		h, p, err := splitHostPort(c.in)
		if c.ok && err != nil {
			t.Errorf("in=%q want ok, got %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("in=%q want err, got host=%q port=%d", c.in, h, p)
		}
		if c.ok && (h != c.host || p != c.port) {
			t.Errorf("in=%q got host=%q port=%d want host=%q port=%d", c.in, h, p, c.host, c.port)
		}
	}
}

func argsContain(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
