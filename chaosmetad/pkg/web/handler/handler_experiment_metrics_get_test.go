/*
 * Copyright 2022-2023 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License", "the License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package handler

// handler_experiment_metrics_get_test.go — pure-function unit test for G3 AggregateExperimentMetrics.
// Covers the codex P2 guard: we must never report a fabricated latency, and counts must be exact.

import (
	"testing"

	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/storage"
)

func TestAggregateExperimentMetrics(t *testing.T) {
	// Mix: 3 success (resident), 1 paused (resident), 2 destroyed (clean-recovered), 1 error (fail),
	// across two target/fault combos.
	exps := []*storage.Experiment{
		{Target: "node", Fault: "cpu", Status: "success"},
		{Target: "node", Fault: "cpu", Status: "success"},
		{Target: "node", Fault: "mem", Status: "success"},
		{Target: "node", Fault: "mem", Status: "paused"},
		{Target: "pod", Fault: "kill", Status: "destroyed"},
		{Target: "pod", Fault: "kill", Status: "destroyed"},
		{Target: "pod", Fault: "loss", Status: "error"},
	}
	got := AggregateExperimentMetrics(exps)

	if got.Total != 7 {
		t.Fatalf("Total=%d want 7", got.Total)
	}
	// resident = success + paused = 4
	if got.Resident != 4 {
		t.Fatalf("Resident=%d want 4", got.Resident)
	}
	if got.Inject == nil || got.Inject.Success != 4 || got.Inject.Fail != 1 || got.Inject.Total != 5 {
		t.Fatalf("Inject breakdown wrong: %+v", got.Inject)
	}
	if got.Recover == nil || got.Recover.Total != 2 {
		t.Fatalf("Recover.Total=%d want 2", got.Recover.Total)
	}
	// by target/fault: node/cpu=2(2 resident), node/mem=2(2 resident), pod/kill=2(0), pod/loss=1(0)
	if len(got.ByTargetFault) != 4 {
		t.Fatalf("ByTargetFault len=%d want 4", len(got.ByTargetFault))
	}
	// latency MUST be reported unavailable, never a fake number.
	if got.Latency == nil || got.Latency.Note == "" {
		t.Fatalf("Latency must be a non-empty note (unavailable), got %+v", got.Latency)
	}
}

func TestAggregateExperimentMetrics_Empty(t *testing.T) {
	got := AggregateExperimentMetrics(nil)
	if got.Total != 0 || got.Resident != 0 {
		t.Fatalf("empty set should be zero, got %+v", got)
	}
	if got.Inject != nil && (got.Inject.Total != 0) {
		t.Fatalf("empty inject breakdown should be zero, got %+v", got.Inject)
	}
	// even on empty input, latency must NOT fabricate a number — note must explain unavailability.
	if got.Latency == nil || got.Latency.Note == "" {
		t.Fatalf("empty set latency must still be the unavailable-note, got %+v", got.Latency)
	}
}
