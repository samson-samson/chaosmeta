/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package procmem

import "testing"

func TestFakeManagerFind(t *testing.T) {
	m := NewFakeManager(
		Process{PID: 100, Name: "vllm.entrypoints", Cmd: "python -m vllm.entrypoints.openai.api_server --model llama"},
		Process{PID: 101, Name: "tritonserver", Cmd: "/opt/tritonserver/bin/tritonserver"},
		Process{PID: 102, Name: "bash", Cmd: "bash -c true"},
	)
	got, err := m.Find("vllm")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].PID != 100 {
		t.Fatalf("want vllm process, got %+v", got)
	}
	got, _ = m.Find("triton")
	if len(got) != 1 || got[0].PID != 101 {
		t.Fatalf("want triton process, got %+v", got)
	}
	got, _ = m.Find("")
	if len(got) != 3 {
		t.Fatalf("empty pattern should match all, got %d", len(got))
	}
}

func TestFakeManagerOomScore(t *testing.T) {
	m := NewFakeManager(Process{PID: 100, Cmd: "vllm"})
	prev, err := m.SetOomScoreAdj(100, 800)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if prev != 0 {
		t.Fatalf("prev=%d, want 0", prev)
	}
	if m.OomCurrent[100] != 800 {
		t.Fatalf("OomCurrent[100]=%d", m.OomCurrent[100])
	}
	prev, _ = m.SetOomScoreAdj(100, 0)
	if prev != 800 {
		t.Fatalf("second set should return previous=800, got %d", prev)
	}
	if _, err := m.SetOomScoreAdj(999, 100); err != ErrNotFound {
		t.Fatalf("unknown pid should return ErrNotFound, got %v", err)
	}
}

func TestFakeManagerAllocate(t *testing.T) {
	m := NewFakeManager()
	if _, err := m.AllocateMemory(0); err == nil {
		t.Fatal("zero bytes should error")
	}
	h, err := m.AllocateMemory(1024 * 1024)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	if m.TotalAllocated() != 1024*1024 {
		t.Fatalf("total=%d", m.TotalAllocated())
	}
	if m.OutstandingAllocs() != 1 {
		t.Fatalf("outstanding=%d", m.OutstandingAllocs())
	}
	if err := m.FreeMemory(h); err != nil {
		t.Fatalf("free: %v", err)
	}
	if m.OutstandingAllocs() != 0 {
		t.Fatalf("outstanding after free=%d", m.OutstandingAllocs())
	}
	// double free is silent
	if err := m.FreeMemory(h); err != nil {
		t.Fatalf("double free should be silent: %v", err)
	}
	m.FailOnAlloc = true
	if _, err := m.AllocateMemory(100); err == nil {
		t.Fatal("FailOnAlloc should force error")
	}
}

func TestRegisterReset(t *testing.T) {
	Reset()
	if _, err := Active().Find("x"); err == nil {
		t.Fatal("nil manager should error on Find")
	}
	f := NewFakeManager()
	Register(f)
	if _, err := Active().Find("x"); err != nil {
		t.Fatalf("registered manager should answer Find: %v", err)
	}
	Reset()
	if _, err := Active().Find("x"); err == nil {
		t.Fatal("Reset should restore nil manager")
	}
}
