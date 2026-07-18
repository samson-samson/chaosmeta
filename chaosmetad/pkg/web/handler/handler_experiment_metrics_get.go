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

// handler_experiment_metrics_get.go — G3 (v3.1 §9.5): chaosmetad process-data endpoint (D8).
//
// Returns aggregated injection counts derivable from the EXISTING storage columns (no schema change,
// so none of the 35 injectors had to be touched — minimal blast radius). We deliberately do NOT
// fabricate a latency number: the storage row's update_time is overwritten by the orphan-timer /
// recover steps, so update_time - create_time is NOT a valid latency (codex P2 finding). Latency is
// therefore reported as nil here; callers (platform) surface "暂不可用" instead of a fake value.
//
// This honors the non-functional "优雅降级，给出明确错误提示，不崩溃" requirement: when latency
// truly cannot be computed, we say so rather than lying.
//
// Route: GET /v1/experiment/metrics?uid=&status=&target=&fault=&creator=

import (
	"context"
	"net/http"

	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/storage"
)

// ExperimentMetricsData is the JSON `data` payload of the metrics response.
type ExperimentMetricsData struct {
	// Counts are aggregated over every row matching the query filters.
	Total    int64                     `json:"total"`
	Inject  *InjectionCountBreakdown   `json:"inject"`
	Recover *RecoverCountBreakdown     `json:"recover"`
	Resident int64                     `json:"resident"` // currently-resident faults (status=success/paused)
	ByTargetFault []TargetFaultCounts  `json:"by_target_fault,omitempty"`
	// Latency is intentionally omitted (nil) — see file doc. A non-nil pointer would be a lie.
	Latency *struct {
		Note string `json:"note"`
	} `json:"latency,omitempty"`
}

type InjectionCountBreakdown struct {
	Total   int64 `json:"total"`
	Success int64 `json:"success"` // rows that reached status=success (injected, fault resident at completion)
	Fail    int64 `json:"fail"`    // rows whose terminal status was error
}

type RecoverCountBreakdown struct {
	Total  int64 `json:"total"`  // rows whose lifecycle reached a post-recover state (destroyed)
	FailKept int64 `json:"fail_kept"` // rows still showing error after a recovery attempt (resident fault possibly remains)
}

type TargetFaultCounts struct {
	Target   string `json:"target"`
	Fault    string `json:"fault"`
	Count    int64  `json:"count"`
	Resident int64  `json:"resident"`
}

// ExperimentMetricsGet handles GET /v1/experiment/metrics.
// metricsResponse mirrors the JSON envelope used by the other handlers (Code/Message/Data) but with
// a Data type that fits our metrics payload (QueryResponse.Data is a different concrete type).
type metricsResponse struct {
	Code    int                   `json:"code"`
	Message string                `json:"message"`
	Data    *ExperimentMetricsData `json:"data,omitempty"`
}

func ExperimentMetricsGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	ctx := context.Background()

	uid := r.URL.Query().Get("uid")
	status := r.URL.Query().Get("status")
	target := r.URL.Query().Get("target")
	fault := r.URL.Query().Get("fault")
	creator := r.URL.Query().Get("creator")

	// A large cap so the aggregation sees everything; storage pagination is for the query UI, not metrics.
	const cap = 100000
	db, dbErr := storage.GetExperimentStore()
	if dbErr != nil {
		WriteResponse(ctx, w, &metricsResponse{
			Code: 1, Message: "get experiment store error: " + dbErr.Error(), Data: nil,
		})
		return
	}
	exps, _, qErr := db.QueryByOption(uid, status, target, fault, creator, "", "", 0, cap)
	if qErr != nil {
		WriteResponse(ctx, w, &metricsResponse{
			Code: 1, Message: "query experiments error: " + qErr.Error(), Data: nil,
		})
		return
	}

	data := AggregateExperimentMetrics(exps)
	WriteResponse(ctx, w, &metricsResponse{
		Code: 0, Message: "success", Data: data,
	})
}

// AggregateExperimentMetrics is the pure aggregation function (extracted for unit testing without a DB).
// It reads only the EXISTING columns: Status, Target, Fault. Resident = status success/paused (fault still applied).
func AggregateExperimentMetrics(exps []*storage.Experiment) *ExperimentMetricsData {
	var injectSuccess, injectFail, destroyed, heldValid int64
	var resident int64
	byKey := map[string]*TargetFaultCounts{}

	for _, e := range exps {
		switch e.Status {
		case "success", "paused":
			injectSuccess++
			resident++
		case "error":
			injectFail++
		case "destroyed":
			destroyed++
		}
		key := e.Target + "/" + e.Fault
		tf, ok := byKey[key]
		if !ok {
			tf = &TargetFaultCounts{Target: e.Target, Fault: e.Fault}
			byKey[key] = tf
		}
		tf.Count++
		if e.Status == "success" || e.Status == "paused" {
			tf.Resident++
		}
	}
	// rows that ever were "injected" (reached success OR terminal error) count toward inject total
	heldValid = injectSuccess + injectFail

	byTF := make([]TargetFaultCounts, 0, len(byKey))
	for _, v := range byKey {
		byTF = append(byTF, *v)
	}

	return &ExperimentMetricsData{
		Total:    int64(len(exps)),
		Resident: resident,
		Inject: &InjectionCountBreakdown{
			Total:   heldValid,
			Success: injectSuccess,
			Fail:    injectFail,
		},
		Recover: &RecoverCountBreakdown{
			Total:     destroyed,
			FailKept:  injectFail,
		},
		ByTargetFault: byTF,
		Latency: &struct {
			Note string `json:"note"`
		}{
			// Honest: latency cannot be derived from current columns (update_time is overwritten
			// by recover/orphan-timer writes — codex P2). Reported as nil-equivalent note, not a number.
			Note: "latency_unavailable: storage row lacks a clean duration field; requires inject_duration_ms (future schema add)",
		},
	}
}
