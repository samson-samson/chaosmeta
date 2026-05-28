/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

// Package ai contains fault injectors that target AI inference infrastructure:
// GPU memory, request/response paths to model servers (vLLM, Triton, TGI,
// OpenAI-compatible gateways), KV-cache bearing worker processes, and on-disk
// model artifacts.
//
// Most injectors are wired through small interfaces (gpu.Driver,
// procmem.Manager) so they can run with a real backend on a production node
// or with a fake backend in unit tests on a developer laptop.
package ai

const (
	TargetAI = "ai"

	FaultGPUMem          = "gpumem"
	FaultInferLatency    = "infer_latency"
	FaultTokenDrop       = "token_drop"
	FaultModelLoad       = "model_load"
	FaultKVCachePressure = "kvcache_pressure"
)
