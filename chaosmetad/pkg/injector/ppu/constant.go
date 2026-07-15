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

// Target = "ppu". PPU 注入器集合，封装 chaosmeta_ppu 内核工具。
// PPU 是宿主机硬件加速器，注入只走宿主机路径（container runtime 留空），
// 由 chaosmetad daemonset 的 nsenter 机制落在目标节点宿主机上执行。

const (
	TargetPPU = "ppu"

	FaultPPUBurn        = "burn"
	FaultPPUMemFill     = "memfill"
	FaultPPUMemClock    = "memclock" // 锁显存时钟 -lmc（ZW810E N/A）
	FaultPPUReset       = "reset"
	FaultPPUClock       = "clock"
	FaultPPUPower       = "power"
	FaultPPUComputeMode = "computemode"
	FaultPPUAppClocks   = "appclocks" // 应用时钟 -ac <mem,cu>，恢复存原值 -ac 或 -rac
	// 开关类：注入时切换开关，recover 恢复注入前原值
	FaultPPUVirtMode  = "virtmode"  // -vm 0(NONE)/2(VGPU)
	FaultPPUMig       = "mig"       // -mig 0/1
	FaultPPUMps       = "mps"       // -mps 0/1
	FaultPPUAutoReset = "autoreset" // --auto-reset 0/1
	FaultPPUOverclock = "overclock" // --overclocking 0/1
	FaultPPUEcc       = "ecc"       // -e 0/1（需 reset/reboot 生效）
	// 方向化子故障（独立 fault 名，固定切向，recover 恢复原值）
	FaultPPUVirtVgpu        = "virtvgpu"
	FaultPPUMigEnable       = "migenable"
	FaultPPUMpsEnable       = "mpsenable"
	FaultPPUAutoResetEnable = "autoresetenable"
	FaultPPUOverclockUltra  = "overclockultra"
	FaultPPUEccEnable       = "eccenable"
	// 方向化子故障（关闭/安全向）
	FaultPPUVirtNone         = "virtnone"
	FaultPPUMigDisable       = "migdisable"
	FaultPPUMpsDisable       = "mpsdisable"
	FaultPPUAutoResetDisable = "autoresetdisable"
	FaultPPUOverclockDefault = "overclockdefault"

	PPUExecKey = "chaosmeta_ppu"

	// compute-mode 取值，透传给 ppu-smi -c。
	ComputeModeDefault       = "0"
	ComputeModeExclusiveProc = "1"
	ComputeModeProhibited    = "2"

	// 默认 Burn 百分比（算力占用语义，仅文档/预留）
	DefaultBurnPercent = 100
	// 默认 MemFill：每卡占用的设备显存 MiB（cudaMalloc）
	DefaultMemFillMb = 256
)
