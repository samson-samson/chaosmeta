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

package experiment_instance

import (
	"chaosmeta-platform/pkg/gateway/apiserver/v1alpha1"
	experimentInstanceModel "chaosmeta-platform/pkg/models/experiment_instance"
	"chaosmeta-platform/pkg/service/experiment"
	"chaosmeta-platform/pkg/service/experiment_instance"
	"encoding/json"
	beego "github.com/beego/beego/v2/server/web"
	"strconv"
	"time"
)

type ExperimentInstanceController struct {
	v1alpha1.BeegoOutputController
	beego.Controller
}

func (c *ExperimentInstanceController) GetExperimentInstances() {
	lastInstance := c.GetString("last_instance")
	//scheduleType := c.GetString("schedule_type")
	namespaceId, _ := c.GetInt("namespace_id")
	experimentUUID := c.GetString("experiment_uuid")
	name := c.GetString("name")
	creatorName := c.GetString("creator_name")
	timeType := c.GetString("time_type")
	timeSearchField := c.GetString("time_search_field")
	status := c.GetString("status")
	recentDays, _ := c.GetInt("recent_days", 0)
	startTime, _ := time.Parse(experiment.TimeLayout, c.GetString("start_time"))
	endTime, _ := time.Parse(experiment.TimeLayout, c.GetString("end_time"))
	orderBy := c.GetString("sort")
	page, _ := c.GetInt("page", 1)
	pageSize, _ := c.GetInt("page_size", 10)
	es := experiment_instance.ExperimentInstanceService{}
	total, experiments, err := es.SearchExperimentInstances(lastInstance, experimentUUID, namespaceId, creatorName, name, timeType, timeSearchField, status, recentDays, startTime, endTime, orderBy, page, pageSize)
	if err != nil {
		c.Error(&c.Controller, err)
		return
	}
	c.Success(&c.Controller, ExperimentInstanceListResponse{
		Page:        page,
		PageSize:    pageSize,
		Total:       total,
		Experiments: experiments,
	})
}

func (c *ExperimentInstanceController) GetExperimentInstanceDetail() {
	uuid := c.GetString(":uuid")
	es := experiment_instance.ExperimentInstanceService{}
	experiment, err := es.GetExperimentInstanceByUUID(uuid)
	if err != nil {
		c.Error(&c.Controller, err)
		return
	}
	c.Success(&c.Controller, experiment)
}

func (c *ExperimentInstanceController) GetExperimentInstanceNodes() {
	uuid := c.GetString(":uuid")
	es := experiment_instance.ExperimentInstanceService{}
	total, nodes, err := es.GetWorkflowNodesInstanceInfoByUUID(uuid)
	if err != nil {
		c.Error(&c.Controller, err)
		return
	}
	c.Success(&c.Controller, GetExperimentInstancesResponse{Total: total, WorkflowNodes: nodes})
}

func (c *ExperimentInstanceController) GetExperimentInstanceNode() {
	uuid := c.GetString(":uuid")
	nodeId := c.GetString(":node_id")
	es := experiment_instance.ExperimentInstanceService{}
	nodeDetail, err := es.GetWorkflowNodeInstanceDetailByUUIDAndNodeId(uuid, nodeId)
	if err != nil {
		c.Error(&c.Controller, err)
		return
	}
	c.Success(&c.Controller, GetExperimentInstanceResponse{WorkflowNode: *nodeDetail})
}

func (c *ExperimentInstanceController) GetExperimentInstanceNodeSubtask() {
	uuid := c.GetString(":uuid")
	nodeId := c.GetString(":node_id")
	subtaskId := c.GetString(":subtask_id")
	es := experiment_instance.ExperimentInstanceService{}
	rangeInstance, err := es.GetFaultRangeInstanceByWorkflowNodeInstanceUUID(uuid, nodeId, subtaskId)
	if err != nil {
		c.Error(&c.Controller, err)
		return
	}
	c.Success(&c.Controller, GetFaultRangeInstanceResponse{FaultRangeInstance: *rangeInstance})
}

func (c *ExperimentInstanceController) DeleteExperimentInstance() {
	uuid := c.GetString(":uuid")
	es := experiment_instance.ExperimentInstanceService{}
	if err := es.DeleteExperimentInstanceByUUID(uuid); err != nil {
		c.Error(&c.Controller, err)
		return
	}
	c.Success(&c.Controller, "ok")
}

func (c *ExperimentInstanceController) DeleteExperimentInstances() {
	var reqBody DeleteExperimentInstanceRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &reqBody); err != nil {
		c.Error(&c.Controller, err)
		return
	}
	es := experiment_instance.ExperimentInstanceService{}
	if err := es.DeleteExperimentInstancesByUUID(reqBody.ResultUUIDs); err != nil {
		c.Error(&c.Controller, err)
		return
	}
	c.Success(&c.Controller, "ok")
}

// GetExperimentInstanceLogs returns persisted log lines for an experiment instance (D11 backend).
// Supports level / node filtering and sinceId-based tailing (for the frontend poll fallback).
// follow=1 is accepted for API symmetry but, in this phase, simply returns the same persisted set
// (true incremental SSE requires a live ingest pipeline from the operator — log rows are persisted by
// the stop/escalation path today; full live ingest is a follow-on phase per design §2.4.2 stage B).
func (c *ExperimentInstanceController) GetExperimentInstanceLogs() {
	uuid := c.GetString(":uuid")
	level := c.GetString("level")
	node := c.GetString("node")
	sinceIDStr := c.GetString("sinceId")
	limit, _ := c.GetInt("limit", 1000)
	var sinceID int64
	if sinceIDStr != "" {
		sinceID, _ = strconv.ParseInt(sinceIDStr, 10, 64)
	}
	logs, err := experimentInstanceModel.ListExperimentInstanceLogs(uuid, level, node, sinceID, limit)
	if err != nil {
		c.Error(&c.Controller, err)
		return
	}
	c.Success(&c.Controller, logs)
}

// GetExperimentInstanceMetrics returns aggregated process data for an experiment instance (D12 backend).
// Derived from the workflow node instances' statuses (inject/recover success/fail) plus persisted log
// error counts — a lightweight, always-available source until the chaosmetad metrics endpoint (D8) is
// wired. The shape matches the frontend MetricsData contract.
func (c *ExperimentInstanceController) GetExperimentInstanceMetrics() {
	uuid := c.GetString(":uuid")
	es := experiment_instance.ExperimentInstanceService{}
	_, nodes, err := es.GetWorkflowNodesInstanceInfoByUUID(uuid)
	if err != nil {
		c.Error(&c.Controller, err)
		return
	}

	type nodeBreakdownUnit struct {
		Node    string `json:"node"`
		Inject  int    `json:"inject"`
		Recover int    `json:"recover"`
		Fail    int    `json:"fail"`
	}
	type errUnit struct {
		Type  string `json:"type"`
		Count int    `json:"count"`
	}
	type metricsResp struct {
		SuccessRate float64 `json:"successRate"`
		Total       int     `json:"total"`
		Succeeded   int     `json:"succeeded"`
		Failed      int     `json:"failed"`
		Latency     *struct {
			P50 *int `json:"p50,omitempty"`
			P90 *int `json:"p90,omitempty"`
			P99 *int `json:"p99,omitempty"`
			Max *int `json:"max,omitempty"`
		} `json:"latency,omitempty"`
		Errors *[]errUnit           `json:"errors,omitempty"`
		Nodes  *[]nodeBreakdownUnit `json:"nodes,omitempty"`
	}

	var resp metricsResp
	resp.Total = len(nodes)
	breakdown := []nodeBreakdownUnit{}
	for _, n := range nodes {
		bn := nodeBreakdownUnit{Node: n.Name}
		switch n.Status {
		case "Succeeded", "succeeded", "success":
			resp.Succeeded++
			bn.Inject = 1
			bn.Recover = 1
		case "Failed", "failed", "error", "Error":
			resp.Failed++
			bn.Inject = 1
			bn.Fail = 1
		case "Running", "running":
			bn.Inject = 1
		}
		breakdown = append(breakdown, bn)
	}
	if resp.Total > 0 {
		resp.SuccessRate = float64(resp.Succeeded) / float64(resp.Total)
	}
	resp.Nodes = &breakdown

	// Error counts from persisted error-level logs, grouped by a coarse type.
	errLogs, _ := experimentInstanceModel.ListExperimentInstanceLogs(uuid, "error", "", 0, 10000)
	if len(errLogs) > 0 {
		bucket := map[string]int{}
		for _, l := range errLogs {
			key := "unknown"
			if l.Phase != "" {
				key = l.Phase
			}
			bucket[key]++
		}
		eu := make([]errUnit, 0, len(bucket))
		for k, v := range bucket {
			eu = append(eu, errUnit{Type: k, Count: v})
		}
		resp.Errors = &eu
	}

	c.Success(&c.Controller, resp)
}

var _ = time.Second // keep time import meaningful (response shaping may use it later)
