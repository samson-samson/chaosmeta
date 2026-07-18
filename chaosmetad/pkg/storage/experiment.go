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

package storage

import (
	"errors"
	"fmt"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils"
	"gorm.io/gorm"
	"time"
)

var globalExpStorage *ExperimentStore

type ExperimentStore struct {
	db *dbStorage
}

func GetExperimentStore() (*ExperimentStore, error) {
	if globalExpStorage == nil {
		db, err := newDBStorage()
		if err != nil {
			return nil, fmt.Errorf("newDBStorage error: %s", err.Error())
		}
		globalExpStorage, err = newExperimentStore(db)
		if err != nil {
			return nil, fmt.Errorf("newExperimentStore error: %s", err.Error())
		}
	}

	return globalExpStorage, nil
}

func newExperimentStore(db *dbStorage) (*ExperimentStore, error) {
	if err := db.AutoMigrate(&Experiment{}); err != nil {
		return nil, err
	}

	return &ExperimentStore{db}, nil
}

func (e *ExperimentStore) Insert(exp *Experiment) error {
	nowTime := time.Now().Format(utils.TimeFormat)
	exp.CreateTime, exp.UpdateTime = nowTime, nowTime
	if err := e.db.Model(Experiment{}).
		Create(exp).
		Error; err != nil {
		return err
	}

	return nil
}

func (e *ExperimentStore) Update(exp *Experiment) error {
	exp.UpdateTime = time.Now().Format(utils.TimeFormat)
	if err := e.db.Model(Experiment{}).
		Where("uid = ?", exp.Uid).
		Updates(exp).
		Error; err != nil {
		return err
	}

	return nil
}

func (e *ExperimentStore) UpdateStatus(uid, status string) error {
	if err := e.db.Model(Experiment{}).
		Where("uid = ?", uid).
		Updates(Experiment{Status: status, UpdateTime: time.Now().Format(utils.TimeFormat)}).
		Error; err != nil {
		return err
	}

	return nil
}

func (e *ExperimentStore) UpdateStatusAndErr(uid, status, errMsg string) error {
	if err := e.db.Model(Experiment{}).
		Where("uid = ?", uid).
		Updates(Experiment{Status: status, Error: errMsg, UpdateTime: time.Now().Format(utils.TimeFormat)}).
		Error; err != nil {
		return err
	}

	return nil
}

func (e *ExperimentStore) GetByUid(uid string) (*Experiment, error) {
	var exp = &Experiment{}
	if err := e.db.Model(Experiment{}).
		Where("uid = ?", uid).
		First(exp).
		Error; err != nil {
		//if errors.Is(err, gorm.ErrRecordNotFound) {
		//	return nil, nil
		//}
		return nil, err
	}

	return exp, nil
}

// UpdateOrphan tracks the detached auto-recover timer for an experiment.
// orphanPid=0 + deadline=0 clears it (e.g. after recover completes or timer is killed).
func (e *ExperimentStore) UpdateOrphan(uid string, orphanPid int, deadline int64) error {
	if err := e.db.Model(Experiment{}).
		Where("uid = ?", uid).
		Updates(Experiment{
			OrphanPid:       orphanPid,
			RecoverDeadline: deadline,
			UpdateTime:      time.Now().Format(utils.TimeFormat),
		}).
		Error; err != nil {
		return err
	}

	return nil
}

// ListByStatus returns all experiments matching any of the given statuses (used by startup stale scan).
func (e *ExperimentStore) ListByStatus(statuses ...string) ([]*Experiment, error) {
	var exps []*Experiment
	if len(statuses) == 0 {
		return exps, nil
	}
	if err := e.db.Model(Experiment{}).
		Where("status IN ?", statuses).
		Order("create_time ASC").
		Find(&exps).
		Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	return exps, nil
}

func (e *ExperimentStore) QueryByOption(uid, status, target, fault, creator, cr, cId string, offset, limit uint) ([]*Experiment, int64, error) {
	var exps []*Experiment
	db := e.db.Model(Experiment{})

	if uid != "" {
		db = db.Where("uid = ?", uid)
	}

	if status != "" {
		db = db.Where("status = ?", status)
	}

	if creator != "" {
		db = db.Where("creator = ?", creator)
	}

	if target != "" {
		db = db.Where("target = ?", target)
	}

	if fault != "" {
		db = db.Where("fault = ?", fault)
	}

	if cr != "" {
		db = db.Where("container_runtime = ?", cr)
	}

	if cId != "" {
		db = db.Where("container_id = ?", cId)
	}

	var total int64
	if err := db.
		Count(&total).
		Order("create_time DESC").
		Offset(int(offset)).Limit(int(limit)).
		Find(&exps).
		Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, err
	}

	return exps, total, nil
}
