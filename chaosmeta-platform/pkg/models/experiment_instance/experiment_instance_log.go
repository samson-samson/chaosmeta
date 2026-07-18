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
	models "chaosmeta-platform/pkg/models/common"
	"github.com/beego/beego/v2/client/orm"
)

// ExperimentInstanceLog persists a single real-time log line for an experiment instance.
// Addresses design D11 persistence: logs are collected while an experiment is running AND after
// it terminates, so they remain queryable for post-mortem review. The frontend streams these via SSE.
type ExperimentInstanceLog struct {
	ID                     int64  `json:"id" orm:"column(id);pk;auto"`
	ExperimentUUID         string `json:"experiment_uuid" orm:"index;column(experiment_uuid);size(64)"`
	ExperimentInstanceUUID string `json:"experiment_instance_uuid" orm:"index;column(experiment_instance_uuid);size(64)"`
	Node                   string `json:"node" orm:"column(node);size(128);default('')"`    // node name / IP
	Level                  string `json:"level" orm:"column(level);size(16);default(info)"` // info|warn|error
	Phase                  string `json:"phase" orm:"column(phase);size(16);default('')"`   // inject|recover|pause
	TraceID                string `json:"trace_id" orm:"column(trace_id);size(64);default('')"`
	Message                string `json:"message" orm:"column(message);type(text)"`
	models.BaseTimeModel
}

func (l *ExperimentInstanceLog) TableName() string {
	return TablePrefix + "instance_log"
}

// CreateExperimentInstanceLog inserts a log line (best-effort; collection failures degrade by counting, never crash main flow).
func CreateExperimentInstanceLog(l *ExperimentInstanceLog) error {
	_, err := models.GetORM().Insert(l)
	return err
}

// BatchCreateExperimentInstanceLogs inserts many log lines in one call to reduce per-line DB round-trips.
func BatchCreateExperimentInstanceLogs(logs []*ExperimentInstanceLog) error {
	if len(logs) == 0 {
		return nil
	}
	_, err := models.GetORM().InsertMulti(len(logs), logs)
	return err
}

// ListExperimentInstanceLogs returns logs for an instance, optionally filtered by level/node, ordered oldest-first.
// sinceID > 0 returns only rows with id > sinceID (for tailing/SSE resume).
func ListExperimentInstanceLogs(instanceUUID, level, node string, sinceID int64, limit int) ([]*ExperimentInstanceLog, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	logs := []*ExperimentInstanceLog{}
	qs := models.GetORM().QueryTable(new(ExperimentInstanceLog).TableName()).Filter("experiment_instance_uuid", instanceUUID)
	if level != "" {
		qs = qs.Filter("level", level)
	}
	if node != "" {
		qs = qs.Filter("node", node)
	}
	if sinceID > 0 {
		qs = qs.Filter("id__gt", sinceID)
	}
	_, err := qs.OrderBy("id").Limit(limit).All(&logs)
	if err == orm.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return logs, nil
}

// CountExperimentInstanceLogs returns how many logs exist for an instance (for metrics / UI badge).
func CountExperimentInstanceLogs(instanceUUID string) (int64, error) {
	c, err := models.GetORM().QueryTable(new(ExperimentInstanceLog).TableName()).Filter("experiment_instance_uuid", instanceUUID).Count()
	if err == orm.ErrNoRows {
		return 0, nil
	}
	return c, err
}
