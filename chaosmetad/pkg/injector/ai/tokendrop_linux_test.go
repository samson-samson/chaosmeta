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
)

func TestTokenDropValidator(t *testing.T) {
	i := &TokenDropInjector{}
	i.SetDefault()
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("empty args should fail")
	}
	i.Args = TokenDropArgs{ListenAddr: ":18081", Upstream: "http://127.0.0.1:8000"}
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("rate=0 should fail")
	}
	i.Args.DropRate = 0.5
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("valid args: %v", err)
	}
	i.Args.DropRate = 1.5
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("rate > 1 should fail")
	}
}

func TestTokenDropInjectRecover(t *testing.T) {
	f := &fakeStarter{NextPID: 7000}
	restore := withTestStarter(f)
	defer restore()

	i := &TokenDropInjector{}
	i.SetDefault()
	i.Args = TokenDropArgs{
		ListenAddr: ":18081",
		Upstream:   "http://127.0.0.1:8000",
		DropRate:   0.25,
		PathPrefix: "/v1/",
		Seed:       12345,
	}
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if i.Runtime.PID != 7000 {
		t.Fatalf("pid=%d", i.Runtime.PID)
	}
	if len(f.StartArr) != 1 {
		t.Fatalf("starts=%d", len(f.StartArr))
	}
	call := f.StartArr[0]
	if call.Name != FaultTokenDrop {
		t.Errorf("name=%q", call.Name)
	}
	if !argsContain(call.Args, "--rate", "0.25") {
		t.Errorf("rate not in args: %v", call.Args)
	}
	if !argsContain(call.Args, "--seed", "12345") {
		t.Errorf("seed not in args: %v", call.Args)
	}
	if err := i.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(f.StopArr) != 1 || f.StopArr[0] != 7000 {
		t.Errorf("Stop=%v", f.StopArr)
	}
}
