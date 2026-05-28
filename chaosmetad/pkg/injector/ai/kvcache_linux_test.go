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
	"testing"

	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector/ai/procmem"
)

func TestKVCacheValidator(t *testing.T) {
	i := &KVCachePressureInjector{}
	i.SetDefault()
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("empty pattern should fail")
	}
	i.Args.ProcessPattern = "vllm"
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("simple pattern should pass: %v", err)
	}
	i.Args.OomScoreAdj = 1500
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("oom_score_adj out of range should fail")
	}
	i.Args.OomScoreAdj = -2000
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("negative oom_score_adj out of range should fail")
	}
}

func TestKVCacheSetDefault(t *testing.T) {
	i := &KVCachePressureInjector{}
	i.SetDefault()
	if i.Args.OomScoreAdj != 500 {
		t.Fatalf("default oom_score_adj=%d, want 500", i.Args.OomScoreAdj)
	}
}

func TestKVCacheInjectRecover(t *testing.T) {
	m := procmem.NewFakeManager(
		procmem.Process{PID: 100, Name: "python", Cmd: "python -m vllm.entrypoints.openai.api_server --model llama"},
		procmem.Process{PID: 101, Name: "tritonserver", Cmd: "/opt/tritonserver/bin/tritonserver --model-repository=/models"},
		procmem.Process{PID: 102, Name: "sshd", Cmd: "/usr/sbin/sshd"},
	)
	procmem.Register(m)
	defer procmem.Reset()

	i := &KVCachePressureInjector{}
	i.SetDefault()
	i.Args = KVCachePressureArgs{
		ProcessPattern: "vllm",
		PressureMiB:    64,
		OomScoreAdj:    750,
	}
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if got := m.OomCurrent[100]; got != 750 {
		t.Errorf("pid 100 oom_score_adj=%d, want 750", got)
	}
	if _, set := m.OomCurrent[101]; set {
		t.Error("pid 101 should not be touched (pattern was 'vllm')")
	}
	if m.TotalAllocated() != 64*1024*1024 {
		t.Errorf("allocated=%d", m.TotalAllocated())
	}
	if i.Runtime.MemHandle == "" {
		t.Error("runtime should record mem handle")
	}
	if len(i.Runtime.PIDsBefore) != 1 {
		t.Errorf("runtime PIDsBefore=%d", len(i.Runtime.PIDsBefore))
	}
	if err := i.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if m.OomCurrent[100] != 0 {
		t.Errorf("after recover oom_score_adj=%d, want 0", m.OomCurrent[100])
	}
	if m.TotalAllocated() != 0 {
		t.Errorf("after recover allocated=%d, want 0", m.TotalAllocated())
	}
}

func TestKVCacheNoMatch(t *testing.T) {
	m := procmem.NewFakeManager(procmem.Process{PID: 1, Cmd: "init"})
	procmem.Register(m)
	defer procmem.Reset()
	i := &KVCachePressureInjector{}
	i.SetDefault()
	i.Args.ProcessPattern = "nonexistent"
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err == nil {
		t.Fatal("no-match pattern should fail Inject")
	}
}

func TestKVCacheAllocFailRollsBack(t *testing.T) {
	m := procmem.NewFakeManager(procmem.Process{PID: 200, Cmd: "vllm"})
	m.FailOnAlloc = true
	procmem.Register(m)
	defer procmem.Reset()
	i := &KVCachePressureInjector{}
	i.SetDefault()
	i.Args = KVCachePressureArgs{ProcessPattern: "vllm", PressureMiB: 32, OomScoreAdj: 600}
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err == nil {
		t.Fatal("Inject should fail when allocation fails")
	}
	// Rollback: oom_score_adj restored, no allocation outstanding.
	if m.OomCurrent[200] != 0 {
		t.Errorf("rollback should restore oom_score_adj, got %d", m.OomCurrent[200])
	}
	if m.OutstandingAllocs() != 0 {
		t.Errorf("rollback should leave 0 allocs, got %d", m.OutstandingAllocs())
	}
}

func TestKVCachePressureOnly(t *testing.T) {
	// PressureMiB=0: oom_score_adj-only mode (skip mem allocation).
	m := procmem.NewFakeManager(procmem.Process{PID: 300, Cmd: "vllm"})
	procmem.Register(m)
	defer procmem.Reset()
	i := &KVCachePressureInjector{}
	i.SetDefault()
	i.Args = KVCachePressureArgs{ProcessPattern: "vllm", PressureMiB: 0, OomScoreAdj: 900}
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if m.OomCurrent[300] != 900 {
		t.Errorf("oom=%d", m.OomCurrent[300])
	}
	if m.TotalAllocated() != 0 {
		t.Errorf("should not allocate when PressureMiB=0, got %d", m.TotalAllocated())
	}
	if i.Runtime.MemHandle != "" {
		t.Errorf("should not have mem handle, got %q", i.Runtime.MemHandle)
	}
	if err := i.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if m.OomCurrent[300] != 0 {
		t.Errorf("post-recover oom=%d", m.OomCurrent[300])
	}
}
