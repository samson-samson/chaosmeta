/*
 * Copyright 2022-2023 ChaosMeta Authors.
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

package ppu

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/log"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils/cmdexec"
)

// ============================ public helper ============================

// runPpuTool 在宿主机上以 `method/fault/args` 调用 chaosmeta_ppu 内核工具。
// PPU 必须走宿主机（containerRuntime 留空），用 ExecTool 的统一出参格式：
//
//	chaosmeta_ppu <method> <fault> <level> <args...>
func runPpuTool(ctx context.Context, method, fault, args string) error {
	e := &cmdexec.CmdExecutor{
		ToolKey: PPUExecKey,
		Method:  method,
		Fault:   fault,
		Args:    args,
	}
	return e.ExecTool(ctx)
}

// ============================ Burn ============================
// 算力满载：targetIds + 百分比。目前用 dmon 持续采样制造设备忙，Recover 杀进程。

func init() {
	injector.Register(TargetPPU, FaultPPUBurn, func() injector.IInjector { return &BurnInjector{} })
}

type BurnInjector struct {
	injector.BaseInjector
	Args    BurnArgs
	Runtime BurnRuntime
}

type BurnArgs struct {
	Ids     string `json:"ids"`
	Percent int    `json:"percent"`
}

type BurnRuntime struct{}

func (i *BurnInjector) GetArgs() interface{}    { return &i.Args }
func (i *BurnInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *BurnInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Ids, "ids", "i", "", "PPU index list, eg: \"0,1,2\" or \"0-3\" or \"all\" (default all)")
	cmd.Flags().IntVarP(&i.Args.Percent, "percent", "p", 0, "burn percent (0 means 100), reserved for CUDA burn")
}

func (i *BurnInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	if i.Args.Ids == "" {
		i.Args.Ids = "all"
	}
	if i.Args.Percent == 0 {
		i.Args.Percent = DefaultBurnPercent
	}
}

func (i *BurnInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.Percent <= 0 || i.Args.Percent > 100 {
		return fmt.Errorf("\"percent\"[%d] must be in (0,100]", i.Args.Percent)
	}
	return runPpuTool(ctx, utils.MethodValidator, FaultPPUBurn, i.Args.Ids)
}

func (i *BurnInjector) Inject(ctx context.Context) error {
	args := fmt.Sprintf("%s %s %d", i.Args.Ids, i.Info.Uid, i.Args.Percent)
	if err := runPpuTool(ctx, utils.MethodInject, FaultPPUBurn, args); err != nil {
		if err := i.Recover(ctx); err != nil {
			log.GetLogger(ctx).Warnf("undo error: %s", err)
		}
		return err
	}
	return nil
}

func (i *BurnInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	return runPpuTool(ctx, utils.MethodRecover, FaultPPUBurn, i.Info.Uid)
}

// ============================ MemFill ============================
// 真 CUDA 显存占用：targetIds + mb(MiB)。exec 工具起 chaosmeta_ppumem（CUDA 程序）
// 在每张目标卡上 cudaMalloc 占住 mb MiB 设备显存，常驻到被 kill 释放。
// 占用可通过 ppu-smi --query-ppu=memory.used 可观测。recover 杀掉记录的 ppumem 进程。

func init() {
	injector.Register(TargetPPU, FaultPPUMemFill, func() injector.IInjector { return &MemFillInjector{} })
}

type MemFillInjector struct {
	injector.BaseInjector
	Args    MemFillArgs
	Runtime MemFillRuntime
}

type MemFillArgs struct {
	Ids string `json:"ids"`
	Mb  int    `json:"mb"` // 每张目标卡占用的设备显存 MiB
}

type MemFillRuntime struct{}

func (i *MemFillInjector) GetArgs() interface{}    { return &i.Args }
func (i *MemFillInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *MemFillInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Ids, "ids", "i", "", "PPU index list, eg: \"0,1,2\" or \"0-3\" or \"all\" (default all)")
	cmd.Flags().IntVarP(&i.Args.Mb, "mb", "m", 0, "MiB of device memory to occupy per card via cudaMalloc (0 means default 256)")
}

func (i *MemFillInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	if i.Args.Ids == "" {
		i.Args.Ids = "all"
	}
	if i.Args.Mb == 0 {
		i.Args.Mb = DefaultMemFillMb
	}
}

func (i *MemFillInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.Mb <= 0 {
		return fmt.Errorf("\"mb\"[%d] for memfill must be a positive int (MiB of device memory)", i.Args.Mb)
	}
	return runPpuTool(ctx, utils.MethodValidator, FaultPPUMemFill, fmt.Sprintf("%s %d", i.Args.Ids, i.Args.Mb))
}

func (i *MemFillInjector) Inject(ctx context.Context) error {
	args := fmt.Sprintf("%s %s %d", i.Args.Ids, i.Info.Uid, i.Args.Mb)
	if err := runPpuTool(ctx, utils.MethodInject, FaultPPUMemFill, args); err != nil {
		if err := i.Recover(ctx); err != nil {
			log.GetLogger(ctx).Warnf("undo error: %s", err)
		}
		return err
	}
	return nil
}

func (i *MemFillInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	return runPpuTool(ctx, utils.MethodRecover, FaultPPUMemFill, i.Info.Uid)
}

// ============================ Reset ============================
// 卡复位：ppu-smi -r。无需常规 recover（reset 本身即"故障+恢复"）。

func init() {
	injector.Register(TargetPPU, FaultPPUReset, func() injector.IInjector { return &ResetInjector{} })
}

type ResetInjector struct {
	injector.BaseInjector
	Args    ResetArgs
	Runtime ResetRuntime
}

type ResetArgs struct {
	Ids string `json:"ids"`
}

type ResetRuntime struct{}

func (i *ResetInjector) GetArgs() interface{}    { return &i.Args }
func (i *ResetInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *ResetInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Ids, "ids", "i", "", "PPU index list, eg: \"0,1,2\" or \"0-3\". MUST be explicit — \"all\" is rejected (resetting every card on an inference node is dangerous)")
}

func (i *ResetInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	// 故意不为空时默认 all：reset 必须显式指定卡，避免误复位整节点。
	if i.Args.Ids == "" {
		i.Args.Ids = ""
	}
}

func (i *ResetInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.Ids == "" || i.Args.Ids == "all" {
		return fmt.Errorf("reset requires explicit ids (\"all\" rejected for safety); eg: --ids \"0\"")
	}
	return runPpuTool(ctx, utils.MethodValidator, FaultPPUReset, i.Args.Ids)
}

func (i *ResetInjector) Inject(ctx context.Context) error {
	return runPpuTool(ctx, utils.MethodInject, FaultPPUReset, i.Args.Ids)
}

func (i *ResetInjector) Recover(ctx context.Context) error {
	// reset 本身即"故障+复位"，无单独 recover 语义：直接返回成功。
	// （不调用 BaseInjector.Recover：它对 Status=success 的实验会返回 "not implemented"，
	// 而 reset 注入成功后状态正是 success，调用它毫无意义且语义混淆。）
	return nil
}

// ============================ Clock (锁频降速) ============================

func init() {
	injector.Register(TargetPPU, FaultPPUClock, func() injector.IInjector { return &ClockInjector{} })
}

type ClockInjector struct {
	injector.BaseInjector
	Args    ClockArgs
	Runtime ClockRuntime
}

type ClockArgs struct {
	Ids   string `json:"ids"`
	Clock int    `json:"clock"` // MHz
}

type ClockRuntime struct{}

func (i *ClockInjector) GetArgs() interface{}    { return &i.Args }
func (i *ClockInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *ClockInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Ids, "ids", "i", "", "PPU index list, eg: \"0,1,2\" or \"0-3\" or \"all\" (default all)")
	cmd.Flags().IntVar(&i.Args.Clock, "clock", 0, "locked PPU CU clock in MHz (must be positive, lower than default = slowdown)")
}

func (i *ClockInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	if i.Args.Ids == "" {
		i.Args.Ids = "all"
	}
}

func (i *ClockInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.Clock <= 0 {
		return fmt.Errorf("\"clock\"[%d] must be a positive int (MHz)", i.Args.Clock)
	}
	return runPpuTool(ctx, utils.MethodValidator, FaultPPUClock, fmt.Sprintf("%s %d", i.Args.Ids, i.Args.Clock))
}

func (i *ClockInjector) Inject(ctx context.Context) error {
	args := fmt.Sprintf("%s %s %d", i.Args.Ids, i.Info.Uid, i.Args.Clock)
	if err := runPpuTool(ctx, utils.MethodInject, FaultPPUClock, args); err != nil {
		if err := i.Recover(ctx); err != nil {
			log.GetLogger(ctx).Warnf("undo error: %s", err)
		}
		return err
	}
	return nil
}

func (i *ClockInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	return runPpuTool(ctx, utils.MethodRecover, FaultPPUClock, i.Info.Uid)
}

// ============================ Power (降功率) ============================

func init() {
	injector.Register(TargetPPU, FaultPPUPower, func() injector.IInjector { return &PowerInjector{} })
}

type PowerInjector struct {
	injector.BaseInjector
	Args    PowerArgs
	Runtime PowerRuntime
}

type PowerArgs struct {
	Ids   string `json:"ids"`
	Power int    `json:"power"` // Watts
}

type PowerRuntime struct{}

func (i *PowerInjector) GetArgs() interface{}    { return &i.Args }
func (i *PowerInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *PowerInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Ids, "ids", "i", "", "PPU index list, eg: \"0,1,2\" or \"0-3\" or \"all\" (default all)")
	cmd.Flags().IntVar(&i.Args.Power, "power", 0, "power limit in Watts (must be positive; lower than default = throttling)")
}

func (i *PowerInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	if i.Args.Ids == "" {
		i.Args.Ids = "all"
	}
}

func (i *PowerInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.Power <= 0 {
		return fmt.Errorf("\"power\"[%d] must be a positive int (Watts)", i.Args.Power)
	}
	return runPpuTool(ctx, utils.MethodValidator, FaultPPUPower, fmt.Sprintf("%s %d", i.Args.Ids, i.Args.Power))
}

func (i *PowerInjector) Inject(ctx context.Context) error {
	args := fmt.Sprintf("%s %s %d", i.Args.Ids, i.Info.Uid, i.Args.Power)
	if err := runPpuTool(ctx, utils.MethodInject, FaultPPUPower, args); err != nil {
		if err := i.Recover(ctx); err != nil {
			log.GetLogger(ctx).Warnf("undo error: %s", err)
		}
		return err
	}
	return nil
}

func (i *PowerInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	return runPpuTool(ctx, utils.MethodRecover, FaultPPUPower, i.Info.Uid)
}

// ============================ ComputeMode (拒绝算) ============================

func init() {
	injector.Register(TargetPPU, FaultPPUComputeMode, func() injector.IInjector { return &ComputeModeInjector{} })
}

type ComputeModeInjector struct {
	injector.BaseInjector
	Args    ComputeModeArgs
	Runtime ComputeModeRuntime
}

type ComputeModeArgs struct {
	Ids  string `json:"ids"`
	Mode string `json:"mode"`
}

type ComputeModeRuntime struct{}

func (i *ComputeModeInjector) GetArgs() interface{}    { return &i.Args }
func (i *ComputeModeInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *ComputeModeInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Ids, "ids", "i", "", "PPU index list, eg: \"0,1,2\" or \"0-3\" or \"all\" (default all)")
	cmd.Flags().StringVar(&i.Args.Mode, "mode", ComputeModeProhibited, fmt.Sprintf("compute mode: %s(DEFAULT) / %s(EXCLUSIVE_PROCESS) / %s(PROHIBITED)", ComputeModeDefault, ComputeModeExclusiveProc, ComputeModeProhibited))
}

func (i *ComputeModeInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	if i.Args.Ids == "" {
		i.Args.Ids = "all"
	}
	if i.Args.Mode == "" {
		i.Args.Mode = ComputeModeProhibited
	}
}

func (i *ComputeModeInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	switch i.Args.Mode {
	case ComputeModeDefault, ComputeModeExclusiveProc, ComputeModeProhibited:
	default:
		return fmt.Errorf("\"mode\"[%s] must be one of %s/%s/%s", i.Args.Mode, ComputeModeDefault, ComputeModeExclusiveProc, ComputeModeProhibited)
	}
	return runPpuTool(ctx, utils.MethodValidator, FaultPPUComputeMode, fmt.Sprintf("%s %s", i.Args.Ids, i.Args.Mode))
}

func (i *ComputeModeInjector) Inject(ctx context.Context) error {
	args := fmt.Sprintf("%s %s %s", i.Args.Ids, i.Info.Uid, i.Args.Mode)
	if err := runPpuTool(ctx, utils.MethodInject, FaultPPUComputeMode, args); err != nil {
		if err := i.Recover(ctx); err != nil {
			log.GetLogger(ctx).Warnf("undo error: %s", err)
		}
		return err
	}
	return nil
}

func (i *ComputeModeInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	return runPpuTool(ctx, utils.MethodRecover, FaultPPUComputeMode, i.Info.Uid)
}

// ============================ AppClocks (应用时钟降速) ============================
// 设 applications-clocks -ac <memMHz,cuMHz>；recover 在 exec 工具侧存原值并恢复到位
// （exec tool 用 -ac <原值> 设回，失败回退 -rac）。

func init() {
	injector.Register(TargetPPU, FaultPPUAppClocks, func() injector.IInjector { return &AppClocksInjector{} })
}

type AppClocksInjector struct {
	injector.BaseInjector
	Args    AppClocksArgs
	Runtime AppClocksRuntime
}

type AppClocksArgs struct {
	Ids   string `json:"ids"`
	MemCu string `json:"memcu"` // "memMHz,cuMHz" eg "1800,800"
}

type AppClocksRuntime struct{}

func (i *AppClocksInjector) GetArgs() interface{}    { return &i.Args }
func (i *AppClocksInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *AppClocksInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Ids, "ids", "i", "", "PPU index list, eg: \"0,1,2\" or \"0-3\" or \"all\" (default all)")
	cmd.Flags().StringVar(&i.Args.MemCu, "memcu", "", `applications clocks "<memMHz>,<cuMHz>" eg "1800,800"; lower CU = slowdown`)
}

func (i *AppClocksInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	if i.Args.Ids == "" {
		i.Args.Ids = "all"
	}
}

func (i *AppClocksInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.MemCu == "" {
		return fmt.Errorf("\"memcu\" is empty, eg \"1800,800\"")
	}
	parts := strings.SplitN(i.Args.MemCu, ",", 2)
	if len(parts) != 2 {
		return fmt.Errorf("\"memcu\"[%s] must be <memMHz>,<cuMHz> eg \"1800,800\"", i.Args.MemCu)
	}
	for _, p := range parts {
		if v, err := strconv.Atoi(strings.TrimSpace(p)); err != nil || v <= 0 {
			return fmt.Errorf("\"memcu\"[%s] must be positive ints (MHz)", i.Args.MemCu)
		}
	}
	return runPpuTool(ctx, utils.MethodValidator, FaultPPUAppClocks, fmt.Sprintf("%s %s", i.Args.Ids, i.Args.MemCu))
}

func (i *AppClocksInjector) Inject(ctx context.Context) error {
	args := fmt.Sprintf("%s %s %s", i.Args.Ids, i.Info.Uid, i.Args.MemCu)
	if err := runPpuTool(ctx, utils.MethodInject, FaultPPUAppClocks, args); err != nil {
		if err := i.Recover(ctx); err != nil {
			log.GetLogger(ctx).Warnf("undo error: %s", err)
		}
		return err
	}
	return nil
}

func (i *AppClocksInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	return runPpuTool(ctx, utils.MethodRecover, FaultPPUAppClocks, i.Info.Uid)
}

// ============================ MemClock (锁显存时钟, ZW810E N/A) ============================

func init() {
	injector.Register(TargetPPU, FaultPPUMemClock, func() injector.IInjector { return &MemClockInjector{} })
}

type MemClockInjector struct {
	injector.BaseInjector
	Args    MemClockArgs
	Runtime MemClockRuntime
}

type MemClockArgs struct {
	Ids string `json:"ids"`
	Mhz int    `json:"mhz"` // locked memory clock MHz
}
type MemClockRuntime struct{}

func (i *MemClockInjector) GetArgs() interface{}    { return &i.Args }
func (i *MemClockInjector) GetRuntime() interface{} { return &i.Runtime }
func (i *MemClockInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Ids, "ids", "i", "", "PPU index list (default all)")
	cmd.Flags().IntVar(&i.Args.Mhz, "mhz", 0, "locked memory clock in MHz (smaller = lower bandwidth)")
}
func (i *MemClockInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	if i.Args.Ids == "" {
		i.Args.Ids = "all"
	}
	if i.Args.Mhz == 0 {
		i.Args.Mhz = 1000
	}
}
func (i *MemClockInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.Mhz <= 0 {
		return fmt.Errorf("\"mhz\"[%d] must be positive", i.Args.Mhz)
	}
	return runPpuTool(ctx, utils.MethodValidator, FaultPPUMemClock, fmt.Sprintf("%s %d", i.Args.Ids, i.Args.Mhz))
}
func (i *MemClockInjector) Inject(ctx context.Context) error {
	args := fmt.Sprintf("%s %s %d", i.Args.Ids, i.Info.Uid, i.Args.Mhz)
	if err := runPpuTool(ctx, utils.MethodInject, FaultPPUMemClock, args); err != nil {
		if rerr := i.Recover(ctx); rerr != nil {
			log.GetLogger(ctx).Warnf("undo error: %s", rerr)
		}
		return err
	}
	return nil
}
func (i *MemClockInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	return runPpuTool(ctx, utils.MethodRecover, FaultPPUMemClock, i.Info.Uid)
}

// ============================ 开关类 injector（virtmode/mig/mps/autoreset/overclock/ecc） ============================
// 一个通用 Injector 被表驱动注册 6 个 fault。每个 fault 用 --code（0/1，virtmode 0/2）发触发。

type ToggleInjector struct {
	injector.BaseInjector
	Args    ToggleArgs
	Runtime ToggleRuntime
	Fault   string
}

type ToggleArgs struct {
	Ids  string `json:"ids"`
	Code string `json:"code"` // "0"/"1", virtmode 也用 "2"
}
type ToggleRuntime struct{}

func (i *ToggleInjector) GetArgs() interface{}    { return &i.Args }
func (i *ToggleInjector) GetRuntime() interface{} { return &i.Runtime }
func (i *ToggleInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Ids, "ids", "i", "", "PPU index list (default all)")
	cmd.Flags().StringVar(&i.Args.Code, "code", "",
		"switch value: disable=0 / enable=1 (virtmode: NONE=0 / VGPU=2)")
}
func (i *ToggleInjector) SetDefault() {
	i.BaseInjector.SetDefault()
	if i.Args.Ids == "" {
		i.Args.Ids = "all"
	}
	// 方向化子故障：code 由方向固定，不接受用户设置。
	if dc, ok := directionalFaultCode(i.Fault); ok {
		i.Args.Code = dc
	} else if i.Args.Code == "" {
		i.Args.Code = "0"
	}
}
func (i *ToggleInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	return runPpuTool(ctx, utils.MethodValidator, i.Fault, fmt.Sprintf("%s %s", i.Args.Ids, i.Args.Code))
}
func (i *ToggleInjector) Inject(ctx context.Context) error {
	args := fmt.Sprintf("%s %s %s", i.Args.Ids, i.Info.Uid, i.Args.Code)
	if err := runPpuTool(ctx, utils.MethodInject, i.Fault, args); err != nil {
		if rerr := i.Recover(ctx); rerr != nil {
			log.GetLogger(ctx).Warnf("undo error: %s", rerr)
		}
		return err
	}
	return nil
}
func (i *ToggleInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	return runPpuTool(ctx, utils.MethodRecover, i.Fault, i.Info.Uid)
}

// directionalFaultCode 返回方向化子故障的固定注入码值（与 exec tool 的 directionalToggle 对应）。
func directionalFaultCode(fault string) (string, bool) {
	switch fault {
	case FaultPPUVirtVgpu:
		return "2", true
	case FaultPPUMigEnable, FaultPPUMpsEnable, FaultPPUAutoResetEnable, FaultPPUOverclockUltra, FaultPPUEccEnable:
		return "1", true
	case FaultPPUVirtNone, FaultPPUMigDisable, FaultPPUMpsDisable, FaultPPUAutoResetDisable, FaultPPUOverclockDefault:
		return "0", true
	}
	return "", false
}

// 一个 init() 用表驱动批量注册所有开关 fault + 方向化子故障。
func init() {
	all := []string{
		FaultPPUVirtMode, FaultPPUMig, FaultPPUMps, FaultPPUAutoReset, FaultPPUOverclock, FaultPPUEcc,
		FaultPPUVirtVgpu, FaultPPUMigEnable, FaultPPUMpsEnable, FaultPPUAutoResetEnable, FaultPPUOverclockUltra, FaultPPUEccEnable,
		FaultPPUVirtNone, FaultPPUMigDisable, FaultPPUMpsDisable, FaultPPUAutoResetDisable, FaultPPUOverclockDefault,
	}
	for _, fault := range all {
		f := fault
		injector.Register(TargetPPU, f, func() injector.IInjector {
			return &ToggleInjector{Fault: f}
		})
	}
}
