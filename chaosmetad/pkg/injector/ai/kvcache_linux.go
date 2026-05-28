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

	"github.com/spf13/cobra"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector/ai/procmem"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/log"
)

func init() {
	injector.Register(TargetAI, FaultKVCachePressure, func() injector.IInjector { return &KVCachePressureInjector{} })
}

// KVCachePressureInjector pushes a set of inference-worker processes (matched
// by cmdline substring, e.g. "vllm", "tritonserver", "tgi") toward KV-cache
// eviction by:
//
//  1. raising their oom_score_adj so the kernel prefers killing them first;
//  2. allocating and pinning a chunk of host memory.
//
// Recover restores oom_score_adj and frees the allocation. The actual
// /proc-touching is delegated to procmem.Manager so unit tests can plug a
// fake.
type KVCachePressureInjector struct {
	injector.BaseInjector
	Args    KVCachePressureArgs
	Runtime KVCachePressureRuntime
}

type KVCachePressureArgs struct {
	// ProcessPattern is a substring matched against /proc/<pid>/cmdline.
	// Required.
	ProcessPattern string `json:"process_pattern"`
	// PressureMiB is how much host RAM to pin. 0 disables the allocation
	// step (oom_score_adj is still bumped).
	PressureMiB uint64 `json:"pressure_mib,omitempty"`
	// OomScoreAdj is the value written to /proc/<pid>/oom_score_adj.
	// Valid range is [-1000, 1000]; we default to +500 if zero.
	OomScoreAdj int `json:"oom_score_adj,omitempty"`
}

type KVCachePressureRuntime struct {
	// PIDsBefore maps pid -> previous oom_score_adj.
	PIDsBefore map[int]int `json:"pids_before,omitempty"`
	// MemHandle is the allocation handle returned by procmem.
	MemHandle string `json:"mem_handle,omitempty"`
}

func (i *KVCachePressureInjector) GetArgs() interface{}    { return &i.Args }
func (i *KVCachePressureInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *KVCachePressureInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	if i.Args.OomScoreAdj == 0 {
		i.Args.OomScoreAdj = 500
	}
}

func (i *KVCachePressureInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.ProcessPattern, "pattern", "P", "",
		"cmdline substring matching inference workers, e.g. \"vllm\" (required)")
	cmd.Flags().Uint64VarP(&i.Args.PressureMiB, "pressure", "m", 0,
		"host memory to pin in MiB (default 0, oom_score_adj only)")
	cmd.Flags().IntVarP(&i.Args.OomScoreAdj, "oom-score-adj", "o", 0,
		"oom_score_adj value to set on matched processes, in [-1000,1000] (default 500)")
}

func (i *KVCachePressureInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.ProcessPattern == "" {
		return fmt.Errorf("\"pattern\" must be provided")
	}
	if i.Args.OomScoreAdj < -1000 || i.Args.OomScoreAdj > 1000 {
		return fmt.Errorf("\"oom-score-adj\"[%d] must be in [-1000,1000]", i.Args.OomScoreAdj)
	}
	return nil
}

func (i *KVCachePressureInjector) Inject(ctx context.Context) error {
	logger := log.GetLogger(ctx)
	m := procmem.Active()
	procs, err := m.Find(i.Args.ProcessPattern)
	if err != nil {
		return fmt.Errorf("find processes matching %q: %w", i.Args.ProcessPattern, err)
	}
	if len(procs) == 0 {
		return fmt.Errorf("no processes matched pattern %q", i.Args.ProcessPattern)
	}
	if i.Runtime.PIDsBefore == nil {
		i.Runtime.PIDsBefore = make(map[int]int)
	}
	for _, p := range procs {
		prev, err := m.SetOomScoreAdj(p.PID, i.Args.OomScoreAdj)
		if err != nil {
			// Best-effort rollback on what we already touched.
			for pid, before := range i.Runtime.PIDsBefore {
				if _, rerr := m.SetOomScoreAdj(pid, before); rerr != nil {
					logger.Warnf("rollback oom_score_adj pid=%d: %s", pid, rerr.Error())
				}
			}
			i.Runtime.PIDsBefore = nil
			return fmt.Errorf("set oom_score_adj pid=%d: %w", p.PID, err)
		}
		i.Runtime.PIDsBefore[p.PID] = prev
	}
	if i.Args.PressureMiB > 0 {
		bytes := i.Args.PressureMiB * 1024 * 1024
		h, err := m.AllocateMemory(bytes)
		if err != nil {
			// Restore the oom_score_adj changes since pressure failed.
			for pid, before := range i.Runtime.PIDsBefore {
				if _, rerr := m.SetOomScoreAdj(pid, before); rerr != nil {
					logger.Warnf("rollback oom_score_adj pid=%d: %s", pid, rerr.Error())
				}
			}
			i.Runtime.PIDsBefore = nil
			return fmt.Errorf("allocate %d MiB host memory: %w", i.Args.PressureMiB, err)
		}
		i.Runtime.MemHandle = string(h)
	}
	logger.Infof("kvcache_pressure: matched %d procs, pressure=%d MiB", len(procs), i.Args.PressureMiB)
	return nil
}

func (i *KVCachePressureInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	logger := log.GetLogger(ctx)
	m := procmem.Active()
	var firstErr error
	if i.Runtime.MemHandle != "" {
		if err := m.FreeMemory(procmem.Handle(i.Runtime.MemHandle)); err != nil {
			logger.Warnf("free pressure handle: %s", err.Error())
			firstErr = err
		}
		i.Runtime.MemHandle = ""
	}
	for pid, before := range i.Runtime.PIDsBefore {
		if _, err := m.SetOomScoreAdj(pid, before); err != nil {
			logger.Warnf("restore oom_score_adj pid=%d -> %d: %s", pid, before, err.Error())
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	i.Runtime.PIDsBefore = nil
	return firstErr
}
