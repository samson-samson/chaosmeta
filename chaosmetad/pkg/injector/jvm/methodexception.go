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

package jvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/log"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils/filesys"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils/process"
	"os"
)

func init() {
	injector.Register(TargetJVM, FaultMethodException, func() injector.IInjector { return &MethodExceptionInjector{} })
}

type MethodExceptionInjector struct {
	injector.BaseInjector
	Args    MethodExceptionArgs
	Runtime MethodExceptionRuntime
}

type MethodExceptionArgs struct {
	Pid        int    `json:"pid,omitempty"`
	Key        string `json:"key,omitempty"`
	MethodList string `json:"method"` // class@method@"ok",
}

type MethodExceptionRuntime struct {
	AttackPids []int `json:"attack_pids"`
}

func (i *MethodExceptionInjector) GetArgs() interface{} {
	return &i.Args
}

func (i *MethodExceptionInjector) GetRuntime() interface{} {
	return &i.Runtime
}

func (i *MethodExceptionInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().IntVarP(&i.Args.Pid, "pid", "p", 0, "target process's pid")
	cmd.Flags().StringVarP(&i.Args.Key, "key", "k", "", "the key used to grep to get target process, the effect is equivalent to \"ps -ef | grep [key]\". if \"pid\" provided, \"key\" will be ignored")
	cmd.Flags().StringVarP(&i.Args.MethodList, "method", "m", "", "target method of the process, format: \"class1@method1@msg,class1@method2@msg\", eg: com.test.Client@sayHello@error,com.test.Client@sayHello@test")
}

func (i *MethodExceptionInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}

	pidList, err := process.GetPidListByPidOrKeyInContainer(ctx, i.Info.ContainerRuntime, i.Info.ContainerId, i.Args.Pid, i.Args.Key)
	if err != nil {
		return fmt.Errorf("get target process's pid error: %s", err.Error())
	}

	for _, pid := range pidList {
		ifExist, err := filesys.ExistFile(getRuleFile(i.Info.ContainerId, pid))
		if err != nil {
			return fmt.Errorf("check file of process[%d] exist error: %s", pid, err.Error())
		}

		if ifExist {
			return fmt.Errorf("has jvm experiment running in process[%d]", pid)
		}
	}

	_, err = getMethodList(i.Args.MethodList, FaultMethodException)
	if err != nil {
		return fmt.Errorf("\"method\" is invalid: %s", err.Error())
	}

	if err := checkJavaCmd(ctx, i.Info.ContainerRuntime, i.Info.ContainerId); err != nil {
		return fmt.Errorf("check java exec error: %s", err.Error())
	}

	return nil
}

func (i *MethodExceptionInjector) Inject(ctx context.Context) error {
	var (
		pidList []int
		err     error
		logger  = log.GetLogger(ctx)
	)

	pidList, _ = process.GetPidListByPidOrKeyInContainer(ctx, i.Info.ContainerRuntime, i.Info.ContainerId, i.Args.Pid, i.Args.Key)
	logger.Debugf("target pid list: %v", pidList)
	// save target
	i.Runtime.AttackPids = pidList

	var timeout int64
	if i.Info.Timeout != "" {
		timeout, _ = utils.GetTimeSecond(i.Info.Timeout)
	}

	methodListMap, _ := getMethodList(i.Args.MethodList, FaultMethodException)
	ruleBytes, err := json.Marshal(getRuleConfig(methodListMap, timeout))
	if err != nil {
		return fmt.Errorf("get rule file bytes error: %s", err.Error())
	}

	logger.Debugf("rule json: %s", string(ruleBytes))
	err = doInject(ctx, i.Info.ContainerRuntime, i.Info.ContainerId, pidList, ruleBytes)
	if err != nil {
		// undo recover
		if rErr := i.Recover(ctx); rErr != nil {
			logger.Warnf("undo error: %s", rErr.Error())
		}
	}

	return err
}

func (i *MethodExceptionInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}

	logger := log.GetLogger(ctx)
	var errMsg string
	for _, pid := range i.Runtime.AttackPids {
		targetRule := getRuleFile(i.Info.ContainerId, pid)
		logger.Debugf("check file: %s", targetRule)
		ifExist, err := filesys.ExistFile(targetRule)
		if err != nil {
			errMsg = fmt.Sprintf("%s. %s", errMsg, fmt.Sprintf("check file[%s] exist error: %s", targetRule, err.Error()))
			continue
		}

		if ifExist {
			if i.Info.ContainerRuntime != "" {
				if err := filesys.RemoveFile(ctx, i.Info.ContainerRuntime, i.Info.ContainerId, getContainerRuleFile(pid)); err != nil {
					errMsg = fmt.Sprintf("%s. %s", errMsg, fmt.Sprintf("remove rule[%s] error: %s", targetRule, err.Error()))
					continue
				}
			}
			if err := os.RemoveAll(targetRule); err != nil {
				errMsg = fmt.Sprintf("%s. %s", errMsg, fmt.Sprintf("remove rule[%s] error: %s", targetRule, err.Error()))
			}
		}
	}

	if errMsg != "" {
		return errors.New(errMsg)
	}

	return nil
}

func getMethodExceptionRule(methodName, valueStr string) (*MethodJVMRule, error) {
	return &MethodJVMRule{
		Method:  methodName,
		Fault:   InsertAtInject,
		LineNum: 0,
		Content: fmt.Sprintf("throw new Exception(\"%s\");", valueStr),
	}, nil
}
