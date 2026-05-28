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
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector/ai/gpu"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/log"
)

func init() {
	injector.Register(TargetAI, FaultGPUMem, func() injector.IInjector { return &GPUMemInjector{} })
}

// GPUMemInjector reserves GPU memory on one or more devices to starve real
// inference workers. The actual allocation is delegated to the registered
// gpu.Driver, which is the null driver on developer machines (so injection
// reports a clean error) and an NVML-backed driver on production nodes.
type GPUMemInjector struct {
	injector.BaseInjector
	Args    GPUMemArgs
	Runtime GPUMemRuntime
}

// GPUMemArgs are user-facing. Size is in MiB to match common operator UX.
// Devices is a comma-separated list of GPU indices, e.g. "0,1". Empty means
// device 0 only.
type GPUMemArgs struct {
	SizeMiB uint64 `json:"size_mib"`
	Devices string `json:"devices,omitempty"`
}

// GPUMemRuntime tracks the handles created during Inject so Recover can
// reliably free them, even after a process restart that reloads the
// experiment from storage.
type GPUMemRuntime struct {
	Handles []string `json:"handles,omitempty"`
}

func (i *GPUMemInjector) GetArgs() interface{}    { return &i.Args }
func (i *GPUMemInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *GPUMemInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().Uint64VarP(&i.Args.SizeMiB, "size", "s", 0,
		"GPU memory to allocate per device, in MiB (required, > 0)")
	cmd.Flags().StringVarP(&i.Args.Devices, "devices", "d", "",
		"comma-separated GPU indices, e.g. \"0,1\" (default \"0\")")
}

func (i *GPUMemInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.SizeMiB == 0 {
		return fmt.Errorf("\"size\" must be > 0")
	}
	if _, err := parseDeviceList(i.Args.Devices); err != nil {
		return fmt.Errorf("\"devices\" is invalid: %s", err.Error())
	}
	return nil
}

func (i *GPUMemInjector) Inject(ctx context.Context) error {
	logger := log.GetLogger(ctx)
	devices, _ := parseDeviceList(i.Args.Devices)
	bytes := i.Args.SizeMiB * 1024 * 1024
	d := gpu.Active()
	logger.Infof("gpumem inject driver=%s devices=%v size_mib=%d", d.Name(), devices, i.Args.SizeMiB)

	var allocated []gpu.Handle
	for _, dev := range devices {
		h, err := d.Allocate(dev, bytes)
		if err != nil {
			for _, ah := range allocated {
				if rerr := d.Free(ah); rerr != nil {
					logger.Warnf("rollback free handle %q failed: %s", ah, rerr.Error())
				}
			}
			return fmt.Errorf("allocate %d MiB on device %d: %w", i.Args.SizeMiB, dev, err)
		}
		allocated = append(allocated, h)
	}
	i.Runtime.Handles = make([]string, 0, len(allocated))
	for _, h := range allocated {
		i.Runtime.Handles = append(i.Runtime.Handles, string(h))
	}
	return nil
}

func (i *GPUMemInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	logger := log.GetLogger(ctx)
	d := gpu.Active()
	var firstErr error
	for _, h := range i.Runtime.Handles {
		if err := d.Free(gpu.Handle(h)); err != nil {
			logger.Warnf("free handle %q: %s", h, err.Error())
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	i.Runtime.Handles = nil
	return firstErr
}

func parseDeviceList(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return []int{0}, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	seen := make(map[int]bool)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("device %q is not an integer", p)
		}
		if n < 0 {
			return nil, fmt.Errorf("device %d must be >= 0", n)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty device list")
	}
	return out, nil
}
