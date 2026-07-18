/*
 * Copyright 2022-2023 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
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

package v1alpha1

import (
	"testing"
	"time"
)

func TestConvertDuration(t *testing.T) {
	type args struct {
		d string
	}
	tests := []struct {
		name    string
		args    args
		want    time.Duration
		wantErr bool
	}{
		{
			name: "value error",
			args: args{
				d: "fe3",
			},
			wantErr: true,
		},
		{
			name: "unit error",
			args: args{
				d: "5p",
			},
			wantErr: true,
		},
		{
			name: "s, true",
			args: args{
				d: "5s",
			},
			want:    time.Second * 5,
			wantErr: false,
		},
		{
			name: "m, true",
			args: args{
				d: "5m",
			},
			want:    time.Minute * 5,
			wantErr: false,
		},
		{
			name: "h, true",
			args: args{
				d: "10h",
			},
			want:    time.Hour * 10,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertDuration(tt.args.d)
			if (err != nil) != tt.wantErr {
				t.Errorf("convertDuration() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("convertDuration() got = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestValidateUpdate_G0 covers the G0 (v3.1 §9.1) webhook放开: a running/paused/error experiment
// must be allowed to flip targetPhase to recover (=stop) or, for running, to pause. The hard Task-2
// requirement "stop recovers from ANY state" depends on these paths being accepted, NOT rejected.
// It also asserts the safety invariants still hold: injection config cannot change mid-run, only
// targetPhase may change, and reverse transitions (recover→inject) are rejected.
func TestValidateUpdate_G0(t *testing.T) {
	// baseSpec is a valid, fully-populated spec shared by old/new; only targetPhase varies in cases.
	mkSpec := func(targetPhase PhaseType) ExperimentSpec {
		return ExperimentSpec{TargetPhase: targetPhase}
	}
	mk := func(oldPhase PhaseType, oldStatus StatusType, newTarget PhaseType) (old, cur *Experiment) {
		o := &Experiment{Spec: mkSpec(InjectPhaseType), Status: ExperimentStatus{Phase: oldPhase, Status: oldStatus}}
		c := &Experiment{Spec: mkSpec(newTarget), Status: ExperimentStatus{Phase: oldPhase, Status: oldStatus}}
		return o, c
	}

	tests := []struct {
		name    string
		old     PhaseType
		status  StatusType
		newTgt  PhaseType
		wantErr bool
	}{
		// G0 放开: running → recover (stop) now allowed
		{"running->recover allowed", InjectPhaseType, RunningStatusType, RecoverPhaseType, false},
		// codex-review-2: running → pause is REJECTED because the operator has no pause phase handler
		// (solveFinalStatus only acts on recover; pause would be an empty shell). Kept closed on purpose.
		{"running->pause rejected (no pause handler yet)", InjectPhaseType, RunningStatusType, PausePhaseType, true},
		// G0 放开: error → recover allowed (fault may be resident; this is the whole point)
		{"error->recover allowed", InjectPhaseType, ErrorStatusType, RecoverPhaseType, false},
		// G0 放开: paused → recover allowed
		{"paused->recover allowed", InjectPhaseType, PausedStatusType, RecoverPhaseType, false},
		// terminal inject states: only recover allowed (legacy)
		{"success->recover allowed", InjectPhaseType, SuccessStatusType, RecoverPhaseType, false},
		{"failed->recover allowed", InjectPhaseType, FailedStatusType, RecoverPhaseType, false},
		// rejections (safety invariants must still hold)
		{"error->pause rejected (error can only stop)", InjectPhaseType, ErrorStatusType, PausePhaseType, true},
		{"paused->pause rejected", InjectPhaseType, PausedStatusType, PausePhaseType, true},
		{"success->pause rejected", InjectPhaseType, SuccessStatusType, PausePhaseType, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old, cur := mk(tt.old, tt.status, tt.newTgt)
			err := cur.ValidateUpdate(old)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateUpdate() err=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}

	// Separate negative case: "running->inject" above has identical old/new specs (both targetPhase=inject),
	// so ValidateUpdate returns nil via the early DeepEqual(Spec) shortcut — it's NOT a real transition.
	// The genuine safety invariant to assert is: changing the injection CONFIG (Spec.Experiment) mid-run
	// must be rejected regardless of status. This guards the explosion radius ("config not mutable mid-run").
	t.Run("config change rejected from running", func(t *testing.T) {
		old := &Experiment{
			Spec:   ExperimentSpec{TargetPhase: InjectPhaseType, Experiment: &ExperimentCommon{Fault: "cpu"}},
			Status: ExperimentStatus{Phase: InjectPhaseType, Status: RunningStatusType},
		}
		cur := &Experiment{
			// targetPhase unchanged, but the fault definition mutated — must be refused.
			Spec:   ExperimentSpec{TargetPhase: InjectPhaseType, Experiment: &ExperimentCommon{Fault: "mem"}},
			Status: ExperimentStatus{Phase: InjectPhaseType, Status: RunningStatusType},
		}
		if err := cur.ValidateUpdate(old); err == nil {
			t.Fatalf("ValidateUpdate() expected error for mid-run config change, got nil")
		}
	})
}
