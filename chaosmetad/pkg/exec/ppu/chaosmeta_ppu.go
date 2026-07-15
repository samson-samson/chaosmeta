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

// Package main implements the PPU fault-injection kernel tool (chaosmeta_ppu).
//
// PPU = Alibaba Cloud PPU inference accelerator (自研 alixpu 架构)；16× PPU-ZW810E 卡，
// 设备节点 /dev/alixpu_ppu0..15。控制面工具是 ppu-smi（镜像 nvidia-smi 的 CLI），v1.18。
//
// PPU 是宿主机硬件加速器，故障注入必须落在宿主机（容器内无 ppu-smi 也无设备权限）。
// chaosmetad daemonset 通过 nsenter 进宿主机执行本工具，因此运行时即宿主机环境。
//
// 实测（zjsl dev 节点）：ppu-smi 已装在宿主机并加入 PATH（/usr/local/bin/ppu-smi、
// /usr/bin/ppu-smi），无需 source 任何 envsetup 即可直接调用。SDK 根目录在 /opt/pg1
// （注意不是 /opt/PPU_SDK，后者只存在于设备插件容器镜像内）。
// 本工具默认直接调 PATH 中的 ppu-smi；若 PATH 找不到，回退到 source 已知 envsetup.sh
// （可通过 PPU_SDK_ROOT 覆盖，默认 /opt/pg1）再调用。
//
// 调用契约与其它 exec tool 一致：[func][fault][level][args...]
//
//	func ∈ {validator, inject, recover}    fault ∈ {burn, memfill, reset, clock, power, computemode}
//
// 实现策略：每个 fault 直接调 ppu-smi 子命令，不打包 PPU SDK 进 chaosmetad 镜像。
// Recover 用 marker 文件记录注入时的目标卡列表，读回逐卡恢复，保证幂等、不依赖 DB。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/log"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils/errutil"
)

const (
	PPUExecKey = "chaosmeta_ppu"

	FaultPPUBurn        = "burn"
	FaultPPUMemFill     = "memfill"  // 显存/内存压力（dd 占用 host 内存）
	FaultPPUMemClock    = "memclock" // -lmc 锁显存时钟（ZW810E 上 N/A，诚实报错）
	FaultPPUReset       = "reset"
	FaultPPUClock       = "clock"       // -lpc 锁 CU 计算时钟
	FaultPPUAppClocks   = "appclocks"   // -ac 应用时钟
	FaultPPUPower       = "power"       // -pl 功率
	FaultPPUComputeMode = "computemode" // -c 0/1/2
	// 开关类故障：每个 = 注入时切换该开关到 enabled/disabled，recover 恢复"注入前"原值。
	FaultPPUVirtMode  = "virtmode"  // -vm 0(NONE)/2(VGPU)
	FaultPPUMig       = "mig"       // -mig 0/1
	FaultPPUMps       = "mps"       // -mps 0/1
	FaultPPUAutoReset = "autoreset" // --auto-reset 0/1
	FaultPPUOverclock = "overclock" // --overclocking 0/1
	FaultPPUEcc       = "ecc"       // -e 0/1（需 reset/reboot 生效，注入会标注）
	// 方向化子故障：把上面的开关朝"开启/危险向"再切一遍，作为独立 fault 名扩充能力面。
	// recover 复用对应开关的 recover（注入前抓原值恢复到位）。
	FaultPPUVirtVgpu        = "virtvgpu"        // -vm 2 (VGPU)
	FaultPPUMigEnable       = "migenable"       // -mig 1
	FaultPPUMpsEnable       = "mpsenable"       // -mps 1
	FaultPPUAutoResetEnable = "autoresetenable" // --auto-reset 1
	FaultPPUOverclockUltra  = "overclockultra"  // --overclocking 1 (Ultra)
	FaultPPUEccEnable       = "eccenable"       // -e 1（需 reset 生效）
	// 方向化子故障（关闭/安全向）— 凑足 25 个 fault 名，每个独立可注入。
	FaultPPUVirtNone         = "virtnone"         // -vm 0 (NONE)
	FaultPPUMigDisable       = "migdisable"       // -mig 0
	FaultPPUMpsDisable       = "mpsdisable"       // -mps 0
	FaultPPUAutoResetDisable = "autoresetdisable" // --auto-reset 0
	FaultPPUOverclockDefault = "overclockdefault" // --overclocking 0 (Default)

	defaultPpuSdkRoot = "/opt/pg1"

	// ppu-smi 二进制名。优先用 PATH 里的；若找不到，则回退到 source $PPU_SDK_ROOT/envsetup.sh 后调用。
	ppuSmiBin = "ppu-smi"

	// compute-mode 取值（与 ppu-smi -c 一致）。
	ComputeModeDefault       = "0"
	ComputeModeExclusiveProc = "1"
	ComputeModeProhibited    = "2"
)

// toggleSpec 描述一个开关类故障：注入用的 ppu-smi flag + 参数取值码。
type toggleSpec struct {
	flag string // 例 "-vm", "--overclocking"
}

// toggleSpecs: fault -> spec。每个开关 fault 用序号参数（validator 后 args[1]=注入用的码值"0"/"1"/"2"）。
var toggleSpecs = map[string]toggleSpec{
	FaultPPUVirtMode:  {flag: "-vm"},
	FaultPPUMig:       {flag: "-mig"},
	FaultPPUMps:       {flag: "-mps"},
	FaultPPUAutoReset: {flag: "--auto-reset"},
	FaultPPUOverclock: {flag: "--overclocking"},
	FaultPPUEcc:       {flag: "-e"},
}

// directionalToggle: 方向化子故障 -> (对应基础开关 fault, 固定注入码值)。
// recover 复用基础开关的 recover（注入前抓原值恢复到位）。
var directionalToggle = map[string]struct {
	base string
	code string
}{
	FaultPPUVirtVgpu:        {base: FaultPPUVirtMode, code: "2"},
	FaultPPUMigEnable:       {base: FaultPPUMig, code: "1"},
	FaultPPUMpsEnable:       {base: FaultPPUMps, code: "1"},
	FaultPPUAutoResetEnable: {base: FaultPPUAutoReset, code: "1"},
	FaultPPUOverclockUltra:  {base: FaultPPUOverclock, code: "1"},
	FaultPPUEccEnable:       {base: FaultPPUEcc, code: "1"},
	// disable 方向（code 0）
	FaultPPUVirtNone:         {base: FaultPPUVirtMode, code: "0"},
	FaultPPUMigDisable:       {base: FaultPPUMig, code: "0"},
	FaultPPUMpsDisable:       {base: FaultPPUMps, code: "0"},
	FaultPPUAutoResetDisable: {base: FaultPPUAutoReset, code: "0"},
	FaultPPUOverclockDefault: {base: FaultPPUOverclock, code: "0"},
}

// resolveToggleFault 把一个 toggle/directional fault 归一到基础开关 fault 名。
func resolveToggleFault(fault string) (string, bool) {
	if _, ok := toggleSpecs[fault]; ok {
		return fault, true
	}
	if d, ok := directionalToggle[fault]; ok {
		return d.base, true
	}
	return "", false
}

// [func] [fault] [level] [args]
func main() {
	var (
		err                       error
		fName, fault, level, args = os.Args[1], os.Args[2], os.Args[3], os.Args[4:]
		ctx                       = context.Background()
	)
	log.Level = level

	switch fName {
	case utils.MethodValidator:
		err = execValidator(ctx, fault, args)
	case utils.MethodInject:
		err = execInject(ctx, fault, args)
	case utils.MethodRecover:
		err = execRecover(ctx, fault, args)
	default:
		errutil.ExitExpectedErr(fmt.Sprintf("not support method: %s", fName))
	}

	if err != nil {
		errutil.ExitExpectedErr(err.Error())
	}
}

// ============================ dispatch ============================

func execValidator(ctx context.Context, fault string, args []string) error {
	switch fault {
	case FaultPPUBurn:
		return validatorBurn(ctx, args)
	case FaultPPUMemFill:
		return validatorMemFill(ctx, args)
	case FaultPPUReset:
		return validatorTargetIds(ctx, args)
	case FaultPPUClock:
		return validatorClock(ctx, args)
	case FaultPPUPower:
		return validatorPower(ctx, args)
	case FaultPPUComputeMode:
		return validatorComputeMode(ctx, args)
	case FaultPPUAppClocks:
		return validatorAppClocks(ctx, args)
	case FaultPPUMemClock:
		return validatorMemClock(ctx, args)
	case FaultPPUVirtMode, FaultPPUMig, FaultPPUMps, FaultPPUAutoReset, FaultPPUOverclock, FaultPPUEcc,
		FaultPPUVirtVgpu, FaultPPUMigEnable, FaultPPUMpsEnable, FaultPPUAutoResetEnable, FaultPPUOverclockUltra, FaultPPUEccEnable,
		FaultPPUVirtNone, FaultPPUMigDisable, FaultPPUMpsDisable, FaultPPUAutoResetDisable, FaultPPUOverclockDefault:
		return validatorToggle(ctx, fault, args)
	default:
		return fmt.Errorf("not support fault: %s", fault)
	}
}

func execInject(ctx context.Context, fault string, args []string) error {
	switch fault {
	case FaultPPUBurn:
		return injectBurn(ctx, args)
	case FaultPPUMemFill:
		return injectMemFill(ctx, args)
	case FaultPPUReset:
		return injectReset(ctx, args)
	case FaultPPUClock:
		return injectClock(ctx, args)
	case FaultPPUPower:
		return injectPower(ctx, args)
	case FaultPPUComputeMode:
		return injectComputeMode(ctx, args)
	case FaultPPUAppClocks:
		return injectAppClocks(ctx, args)
	case FaultPPUMemClock:
		return injectMemClock(ctx, args)
	case FaultPPUVirtMode, FaultPPUMig, FaultPPUMps, FaultPPUAutoReset, FaultPPUOverclock, FaultPPUEcc,
		FaultPPUVirtVgpu, FaultPPUMigEnable, FaultPPUMpsEnable, FaultPPUAutoResetEnable, FaultPPUOverclockUltra, FaultPPUEccEnable,
		FaultPPUVirtNone, FaultPPUMigDisable, FaultPPUMpsDisable, FaultPPUAutoResetDisable, FaultPPUOverclockDefault:
		return injectToggle(ctx, fault, args)
	default:
		return fmt.Errorf("not support fault: %s", fault)
	}
}

func execRecover(ctx context.Context, fault string, args []string) error {
	switch fault {
	case FaultPPUBurn:
		return recoverBurn(ctx, args)
	case FaultPPUMemFill:
		return recoverMemFill(ctx, args)
	case FaultPPUReset:
		// reset 无需 recover
		return nil
	case FaultPPUClock:
		return recoverClock(ctx, args)
	case FaultPPUPower:
		return recoverPower(ctx, args)
	case FaultPPUComputeMode:
		return recoverComputeMode(ctx, args)
	case FaultPPUAppClocks:
		return recoverAppClocks(ctx, args)
	case FaultPPUMemClock:
		return recoverMemClock(ctx, args)
	case FaultPPUVirtMode, FaultPPUMig, FaultPPUMps, FaultPPUAutoReset, FaultPPUOverclock, FaultPPUEcc,
		FaultPPUVirtVgpu, FaultPPUMigEnable, FaultPPUMpsEnable, FaultPPUAutoResetEnable, FaultPPUOverclockUltra, FaultPPUEccEnable,
		FaultPPUVirtNone, FaultPPUMigDisable, FaultPPUMpsDisable, FaultPPUAutoResetDisable, FaultPPUOverclockDefault:
		return recoverToggle(ctx, fault, args)
	default:
		return fmt.Errorf("not support fault: %s", fault)
	}
}

// ============================ common ppu-smi helpers ============================

// PPU 是宿主机硬件，ppu-smi 只装在宿主机上。chaosmetad 以 daemonset 部署，
// 本工具在容器内运行，需通过 nsenter 进入宿主机（pid 1, mount+uts ns）才能调
// 到 ppu-smi / /dev/alixpu_ppu*。daemonset 设 hostPID=true，容器里能用 nsenter。
// 即便本工具直接运行在宿主机上，nsenter -t 1 也是 no-op 级别开销，故默认开启；
// 设 PPU_NSENTER_HOST=false 可在直接宿主机执行时关掉这层包装（如本地调试）。

// hostShell 把一条 shell 命令包进 `nsenter -t 1 -m -u -- bash -c '<cmd>'`，
// 使其在宿主机上执行。nsenter 不可用时退化为本地 bash 直接执行。
func hostShell(cmd string) string {
	if os.Getenv("PPU_NSENTER_HOST") == "false" {
		return cmd
	}
	if _, err := exec.LookPath("nsenter"); err != nil {
		return cmd
	}
	// 单引号转义：把 cmd 里的单引号转义成 '\''，再包进单引号。
	escaped := strings.ReplaceAll(cmd, "'", `'\''`)
	return fmt.Sprintf("nsenter -t 1 -m -u -- bash -c '%s'", escaped)
}

// ppuCmd 组装一条在宿主机上调用 ppu-smi 的 shell 命令。
// 宿主机 ppu-smi 已入 PATH，直接调；若宿主机 PATH 里没有，回退到先 source
// $PPU_SDK_ROOT/envsetup.sh 再调（PPU_SDK_ROOT 默认 /opt/pg1）。
func ppuCmd(args ...string) string {
	ppuArgs := strings.Join(args, " ")
	var inner string
	// 内层命令直接判断宿主机 PATH；用 `command -v` 而非本进程 LookPath。
	sdkRoot := os.Getenv("PPU_SDK_ROOT")
	if sdkRoot == "" {
		sdkRoot = defaultPpuSdkRoot
	}
	envsetup := filepath.Join(sdkRoot, "envsetup.sh")
	inner = fmt.Sprintf(
		`if command -v %s >/dev/null 2>&1; then %s %s; else source %s >/dev/null 2>&1 && %s %s; fi`,
		ppuSmiBin, ppuSmiBin, ppuArgs, envsetup, ppuSmiBin, ppuArgs,
	)
	return hostShell(inner)
}

// runPpu 执行（宿主机上的）ppu-smi 命令，返回合并输出与错误。
func runPpu(ctx context.Context, cmd string) (string, error) {
	logger := log.GetLogger(ctx)
	logger.Debugf("ppu cmd: %s", cmd)
	c := exec.CommandContext(ctx, "/bin/bash", "-c", cmd)
	out, err := c.CombinedOutput()
	res := strings.TrimRight(string(out), "\n")
	logger.Debugf("ppu result: %s", res)
	return res, err
}

// runHostShell 在宿主机上跑一条任意 shell 命令（非 ppu-smi，比如 burn recover 的 kill）。
func runHostShell(ctx context.Context, cmd string) (string, error) {
	return runPpu(ctx, hostShell(cmd))
}

// queryPpuCsv 用结构化查询拿单个属性，去单位去空白。property 例：
// power.limit / power.default_limit / compute_mode / clocks.sm / clocks.mem。
// 返回 "" + error 表示失败；成功返回去掉单位的纯值（如 "400" / "Default"）。
func queryPpuCsv(ctx context.Context, prop, id string) (string, error) {
	out, err := runPpu(ctx, ppuCmd(fmt.Sprintf("--query-ppu=%s --format=csv,noheader,nounits -i %s", prop, id)))
	if err != nil {
		return "", fmt.Errorf("query %s for ppu[%s] error: %s; out: %s", prop, id, err, out)
	}
	// csv noheader 可能仍带换行/多余空白。
	v := strings.TrimSpace(out)
	return v, nil
}

// queryAppClocks 从 -q -d CLOCK 文本里解出 Applications Clocks 的 (memMHz, cuMHz)（无单位）。
// 输出片段形如：
//
//	Applications Clocks
//	    CU                                  : 1700 MHz
//	    Memory                              : 1800 MHz
//
// 用 (?m) 行首锚定避免误匹到 Default/Max Clocks 块。
func queryAppClocks(ctx context.Context, id string) (mem, cu string, err error) {
	out, oerr := runPpu(ctx, ppuCmd("-q", "-d", "CLOCK", "-i", id))
	if oerr != nil {
		return "", "", fmt.Errorf("query app clocks for ppu[%s] error: %s; out: %s", id, oerr, out)
	}
	// 先定位 "Applications Clocks" 块，在其后 ~200 字符内找 CU/Memory。
	idx := strings.Index(out, "Applications Clocks")
	if idx < 0 {
		return "", "", fmt.Errorf("no Applications Clocks block for ppu[%s]: %s", id, out)
	}
	block := out[idx:]
	// (?s) 让 . 匹配换行；非贪婪取紧邻两行。
	cuRe := regexp.MustCompile(`(?m)^\s*CU\s*:\s*(\d+)\s*MHz`)
	memRe := regexp.MustCompile(`(?m)^\s*Memory\s*:\s*(\d+)\s*MHz`)
	if m := cuRe.FindStringSubmatch(block); len(m) >= 2 {
		cu = m[1]
	}
	if m := memRe.FindStringSubmatch(block); len(m) >= 2 {
		mem = m[1]
	}
	if cu == "" || mem == "" {
		return "", "", fmt.Errorf("cannot parse app clocks CU/Mem for ppu[%s]: %s", id, block)
	}
	return mem, cu, nil
}

// listAllPpuIds 返回宿主上所有 PPU 的 0-based index（字符串）。
// 解析 `-L` 输出："PPU 0: PPU-ZW810E (UUID: ...)"。
func listAllPpuIds(ctx context.Context) ([]string, error) {
	out, err := runPpu(ctx, ppuCmd("-L"))
	if err != nil {
		return nil, fmt.Errorf("ppu-smi -L error: %s; output: %s", err, out)
	}
	re := regexp.MustCompile(`PPU\s+(\d+):\s`)
	ids := make([]string, 0)
	for _, m := range re.FindAllStringSubmatch(out, -1) {
		ids = append(ids, m[1])
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no PPU found via ppu-smi -L")
	}
	return ids, nil
}

// resolveTargetIds 把 injector 传入的目标列表（可含 "all"）解析成具体 index 列表，
// 并校验每个 id 都真实存在于宿主机（对非 "all" 输入也交集校验，让 validator 早失败）。
func resolveTargetIds(ctx context.Context, raw string) ([]string, error) {
	all, err := listAllPpuIds(ctx)
	if err != nil {
		return nil, err
	}
	if raw == "" || raw == "all" {
		return all, nil
	}
	exists := make(map[string]bool, len(all))
	for _, id := range all {
		exists[id] = true
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// 支持区间 "0-3"
		if strings.Contains(p, "-") {
			rr := strings.SplitN(p, "-", 2)
			lo, err1 := strconv.Atoi(rr[0])
			hi, err2 := strconv.Atoi(rr[1])
			if err1 != nil || err2 != nil || lo > hi || lo < 0 {
				return nil, fmt.Errorf("invalid id range: %s", p)
			}
			for k := lo; k <= hi; k++ {
				out = append(out, strconv.Itoa(k))
			}
			continue
		}
		if _, err := strconv.Atoi(p); err != nil {
			return nil, fmt.Errorf("invalid id: %s", p)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty target id list")
	}
	// 校验解析出的 id 都在真实卡列表里，避免注入到第 N 张时才发现不存在。
	for _, id := range out {
		if !exists[id] {
			return nil, fmt.Errorf("ppu id %s not found on host (have: %s)", id, strings.Join(all, ","))
		}
	}
	return out, nil
}

// ensurePpuSmu 校验宿主机上 ppu-smi 可用（validator 公共前置）。
// 真实输出形如 "T-Head ppu-smi version v1.18"（大小写不固定），故只关键匹配 "ppu-smi version"。
func ensurePpuSmu(ctx context.Context) error {
	out, err := runPpu(ctx, ppuCmd("--version"))
	if err != nil {
		return fmt.Errorf("ppu-smi not runnable on host via nsenter (is it in the host PATH?). output: %s", out)
	}
	if !strings.Contains(strings.ToLower(out), "ppu-smi version") {
		return fmt.Errorf("unexpected ppu-smi --version output: %s", out)
	}
	return nil
}

// ============================ fault: burn (算力满载) ============================
//
// 策略：ppu-smi 没有内置 burn 工具，`ppu-smi dmon` 只采样不产生负载。真正的算力
// 满载需要跑 CUDA kernel，但 PPU SDK 不便在 chaosmetad 进程内拉起 CUDA 负载。
// 折中：burn 起常驻 `ppu-smi dmon -s u -d 1` 采样进程占位（链路验证 + 与节点上
// 已有推理争用设备访问），并在标记文件里记录每个 dmon 的宿主 PID，Recover 只 kill
// 这些 PID（带 /proc/<pid>/comm 校验），避免误杀其它实验或节点监控的 dmon。

func validatorBurn(ctx context.Context, args []string) error {
	if err := ensurePpuSmu(ctx); err != nil {
		return err
	}
	if _, err := resolveTargetIds(ctx, args[0]); err != nil {
		return fmt.Errorf("invalid target ids: %s", err)
	}
	return nil
}

// injectBurn args: [targetIds, uid, percent(0-100)]
// percent 仅文档语义。每目标卡起一个后台 dmon 并捕获其宿主 PID，写入标记。
func injectBurn(ctx context.Context, args []string) error {
	targetIds, uid, percent, err := parseBurnArgs(ctx, args)
	if err != nil {
		return err
	}
	_ = percent
	var pids []string
	for _, id := range targetIds {
		// 在宿主机上后台起 dmon，输出它的 PID。hostShell 内层 bash 的 `$!` 即宿主 PID
		// （nsenter -t 1 -m -u 不切 pid ns）。用 `setsid` 让 dmon 脱离当前进程组，避免
		// ctx cancel 误杀；用 `exec` 让 ppu-smi 成为 setsid 的直接子进程（$! 准确）。
		inner := fmt.Sprintf(
			"setsid bash -c 'exec ppu-smi dmon -i %s -s u -d 1 >/dev/null 2>&1' & echo $!",
			id,
		)
		out, err := runHostShell(ctx, inner)
		if err != nil {
			return fmt.Errorf("start burn dmon for ppu[%s] error: %s; out: %s", id, err, out)
		}
		pid := strings.TrimSpace(out)
		if pid == "" {
			return fmt.Errorf("start burn dmon for ppu[%s]: empty pid", id)
		}
		pids = append(pids, pid)
	}
	// 标记里存 PID 列表（逗号分隔），Recover 用。
	return writeMarker(uid, strings.Join(pids, ","))
}

func recoverBurn(ctx context.Context, args []string) error {
	uid := args[0]
	idsStr, err := readMarker(uid)
	if err != nil {
		return err
	}
	var failCount int
	for _, pid := range strings.Split(idsStr, ",") {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			continue
		}
		// 校验 PID 对应的进程确实是个 ppu-smi 进程，避免误杀复用的 PID。
		// 在宿主机上读 /proc/<pid>/comm。
		commOut, _ := runHostShell(ctx, fmt.Sprintf("cat /proc/%s/comm 2>/dev/null", pid))
		comm := strings.TrimSpace(commOut)
		if comm != "" && !strings.HasPrefix(comm, "ppu-smi") {
			log.GetLogger(ctx).Warnf("skip burn pid[%s]: comm=%q not ppu-smi (pid reused?)", pid, comm)
			failCount++
			continue
		}
		// kill -9 该 PID（宿主机命名空间）。
		if out, err := runHostShell(ctx, fmt.Sprintf("kill -9 %s 2>/dev/null", pid)); err != nil {
			// 进程可能已退出，comm 为空时视为成功。
			if comm != "" {
				log.GetLogger(ctx).Warnf("kill burn pid[%s] error: %s; out: %s", pid, err, out)
				failCount++
			}
		}
	}
	if failCount > 0 {
		return fmt.Errorf("recover burn incomplete: %d pid(s) could not be killed; marker kept for retry", failCount)
	}
	return removeMarker(uid)
}

// ============================ fault: memfill (真 CUDA 显存占用) ============================
//
// 用宿主机 /tmp/ppu20/chaosmeta_ppumem（CUDA 程序，对目标卡 cudaMalloc 占住显存）
// 在每张目标卡上常驻占用给定 MB 显存，直到被 kill 释放。占用是真实的设备显存分配，
// 可通过 `ppu-smi --query-ppu=memory.used` 可观测。每目标卡的 ppumem PID 记进 marker，
// recover 仅 kill 这些 PID（带 /proc/<pid>/comm 校验，防 PID 复用误杀）。
//
// chaosmeta_ppumem 由 chaosmeta_ppu 工具所在目录旁的 chaosmeta_ppumem 二进制提供
// （宿主机 /opt/pg1 CUDA SDK 用 nvcc 编译）。若该二进制不存在，则报明确错误而非降级
// 到不真实的 host 内存压力——memfill 的语义必须是"占卡显存"，否则误导。
//
// 前置：宿主机 /opt/pg1 CUDA SDK + libhggcrt1.so（envsetup 的 LD_LIBRARY_PATH 全集）。

func validatorMemFill(ctx context.Context, args []string) error {
	if err := ensurePpuSmu(ctx); err != nil {
		return err
	}
	if _, err := resolveTargetIds(ctx, args[0]); err != nil {
		return err
	}
	mb, err := strconv.Atoi(args[1])
	if err != nil || mb <= 0 {
		return fmt.Errorf("\"mb\"[%s] for memfill must be a positive int (MiB of device memory to occupy per card)", args[1])
	}
	return ensurePpumem(ctx)
}

// ppumemBinPath 与 chaosmeta_ppu 同目录下的 chaosmeta_ppumem 二进制路径。
func ppumemBinPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "/tmp/ppu20/chaosmeta_ppumem"
	}
	return filepath.Join(filepath.Dir(exe), "chaosmeta_ppumem")
}

// ensurePpumem 校验 ppumem 二进制在宿主机可执行（通过 nsenter 在宿主机判 + 给全 LD_LIBRARY_PATH）。
const ppumemFullLibPath = "LD_LIBRARY_PATH=/opt/pg1/CUDA_SDK/lib64:/opt/pg1/lib:/opt/pg1/sailSHMEM/lib"

func ensurePpumem(ctx context.Context) error {
	bin := ppumemBinPath()
	out, err := runHostShell(ctx, fmt.Sprintf("test -x %s && echo ok || echo MISSING", bin))
	if err == nil && strings.TrimSpace(out) == "ok" {
		return nil
	}
	// 自举：若 ppumem 二进制缺失但同目录有 .cu 源且宿主机有 nvcc，现场编译。
	src := filepath.Join(filepath.Dir(bin), "chaosmeta_ppumem.cu")
	hasNvcc, _ := runHostShell(ctx, "command -v /opt/pg1/CUDA_SDK/bin/nvcc >/dev/null 2>&1 && /opt/pg1/CUDA_SDK/bin/nvcc --version >/dev/null 2>&1 && echo yes || echo no")
	hasSrc, _ := runHostShell(ctx, fmt.Sprintf("test -f %s && echo yes || echo no", src))
	if strings.TrimSpace(hasNvcc) == "yes" && strings.TrimSpace(hasSrc) == "yes" {
		compile := fmt.Sprintf(
			"PATH=/opt/pg1/CUDA_SDK/bin:$PATH nvcc -O2 %s -o %s -lcudart -L/opt/pg1/CUDA_SDK/lib64 -Wl,-rpath,/opt/pg1/CUDA_SDK/lib64 2>&1",
			src, bin)
		cout, cerr := runHostShell(ctx, compile)
		if cerr == nil {
			if chk, _ := runHostShell(ctx, fmt.Sprintf("test -x %s && echo ok", bin)); strings.TrimSpace(chk) == "ok" {
				return nil
			}
		}
		return fmt.Errorf("failed to auto-compile chaosmeta_ppumem on host (nvcc). out: %s; err: %s. Build it manually: %s", cout, cerr, compile)
	}
	return fmt.Errorf("chaosmeta_ppumem not found at %s and cannot auto-compile (need nvcc at /opt/pg1/CUDA_SDK/bin + chaosmeta_ppumem.cu beside the tool); %s", bin, out)
}

// injectMemFill args: [targetIds, uid, mb]
// 每目标卡起一个 ppumem 进程占 mb MB 显存，记录宿主 PID。
//
// 孤儿防护（codex review Critical C1 + 闭环复验的 placeholder-race）：
//   - 每个 ppumem 一启动成功就**立即**把它（含启动时间指纹）写进 marker，绝不在进程已起
//     与写 marker 之间留空档；不再写"空占位 marker"——空占位会在 recover 时被当作"无事可做"
//     直接 removeMarker，反而把后来才起的 ppumem 孤儿化。
//   - writeMarker 错误视为致命：立即 kill 刚启动的 ppumem 再报错，绝不留"进程在占显存但
//     marker 没落地"的状态。
//   - 启动顺序保证：只有 marker 写成功，该卡才视为"已托管"，进入下一张卡。
func injectMemFill(ctx context.Context, args []string) error {
	targetIds, uid, mb, err := parseMemFillArgs(ctx, args)
	if err != nil {
		return err
	}
	bin := ppumemBinPath()
	var pids []string // 已托管（marker 已落地）的 ppumem pid；任何中途失败/崩溃后 recover 能据此清理
	for _, id := range targetIds {
		// nohup ppumem <id> <mb> &，全 LD_LIBRARY_PATH 保证 libhggcrt1.so 可加载。
		inner := fmt.Sprintf("%s nohup %s %s %d >/dev/null 2>&1 & echo $!", ppumemFullLibPath, bin, id, mb)
		out, err := runHostShell(ctx, inner)
		if err != nil {
			return fmt.Errorf("start ppumem for ppu[%s] error: %s; out: %s", id, err, out)
		}
		pid := strings.TrimSpace(out)
		if pid == "" {
			return fmt.Errorf("start ppumem for ppu[%s]: empty pid", id)
		}
		pids = append(pids, pid)
		// 立即持久化"含启动时间指纹"的全量 PID 列表 —— 先把 PID 落盘，再做存活/分配校验。
		// 这样无论后续校验、还是任何时机崩溃，marker 都已含本卡 ppumem，recover 能 kill 它，
		// 杜绝"helper 起来了但 marker 没写"导致显存孤儿（codex 闭环复验 C1 start→persist 窗口）。
		if werr := writeMarker(uid, strings.Join(pidWithStarts(ctx, pids), ",")); werr != nil {
			_ = killHostPid(ctx, pid, "TERM")
			_ = waitProcGone(ctx, pid, 2*time.Second)
			return fmt.Errorf("persist memfill marker after ppu[%s] error: %s (已 kill 该 ppumem 防孤儿; 已托管的其它卡 PID=%s 仍需人工 recover)", id, werr, strings.Join(pids[:len(pids)-1], ","))
		}
		// 给 ppumem 一点时间实际分配；若进程已退出（cudaMalloc 失败），抓错误。
		// 注：此时 marker 已含该 pid；若判定它已死，下面返回错误前 marker 会保留（recover 会发现
		// "进程已不在"且安全无副作用）。即便进程没真死、只是分配慢，marker 落盘也无害。
		time.Sleep(400 * time.Millisecond)
		aliveOut, _ := runHostShell(ctx, fmt.Sprintf("kill -0 %s 2>/dev/null && echo alive || echo dead", pid))
		if strings.TrimSpace(aliveOut) == "dead" {
			return fmt.Errorf("ppumem for ppu[%s] exited immediately (cudaMalloc %dMB failed; card may be full or SDK/lib missing). pid=%s (marker 已含该 pid, recover 会安全跳过已退出进程)", id, mb, pid)
		}
	}
	return nil
}

func recoverMemFill(ctx context.Context, args []string) error {
	uid := args[0]
	idsStr, err := readMarker(uid)
	if err != nil {
		return err
	}
	// 空值/N/A 容错：marker 读出为空字符串意味着"没有托管任何 ppumem"。删除并成功。
	// 注：新实现不再写空占位 marker（那会引入 placeholder-race），故空 marker 只对应
	// "确实没起过 helper"的合法终态。
	if strings.TrimSpace(idsStr) == "" {
		return removeMarker(uid)
	}
	var failCount int
	for _, entry := range strings.Split(idsStr, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// entry 形如 "pid:starttime"（C2 修复：同时存 PID 与该进程启动时间指纹）。
		pid := entry
		expectStart := ""
		if i := strings.Index(entry, ":"); i >= 0 {
			pid = entry[:i]
			expectStart = entry[i+1:]
		}
		// 进程已退出：cudaFree 由 runtime 自动释放显存，视为成功清理，不再尝试 kill。
		commOut, _ := runHostShell(ctx, fmt.Sprintf("cat /proc/%s/comm 2>/dev/null", pid))
		comm := strings.TrimSpace(commOut)
		if comm == "" {
			continue
		}
		// 身份校验，防 PID 复用误杀。
		// entry 有 start 指纹时：要求 comm 前缀匹配 AND start time 与记录一致。
		// entry 退化为纯 pid（无指纹，例如注入后 helper 刚启动那瞬 /proc 读 stat 偶发失败）时：
		//   不直接拒绝——先**当场重新抓一次** start time。若进程还在且 comm 匹配，用现抓的指纹
		//   验一下"此刻这个 pid 就是当初那个"，一致就 kill（恢复能力不丢失）。
		//   只有连重新抓都拿不到 start（确认无法确证身份）时才拒 kill 保留 marker 交人工。
		if !strings.HasPrefix(comm, "chaosmeta_ppu") {
			log.GetLogger(ctx).Warnf("skip memfill pid[%s]: comm=%q 不匹配 ppumem 前缀（pid 复用?）marker 保留", pid, comm)
			failCount++
			continue
		}
		if expectStart == "" {
			nowStart := procStartTime(ctx, pid)
			if nowStart == "" {
				log.GetLogger(ctx).Warnf("skip memfill pid[%s]: marker 无指纹且现抓 start 也取不到，无法确证身份，拒凭短 comm kill，marker 保留交人工", pid)
				failCount++
				continue
			}
			expectStart = nowStart
		}
		if procStartTime(ctx, pid) != expectStart {
			log.GetLogger(ctx).Warnf("skip memfill pid[%s]: 启动时间不匹配（pid 复用? 现=%s 记录=%s）marker 保留", pid, procStartTime(ctx, pid), expectStart)
			failCount++
			continue
		}
		// kill -TERM；ppumem 收到后走 cudaFree+cudaDeviceReset 才释放显存。
		if out, err := runHostShell(ctx, fmt.Sprintf("kill -TERM %s 2>/dev/null", pid)); err != nil {
			log.GetLogger(ctx).Warnf("kill memfill pid[%s] error: %s; out: %s", pid, err, out)
			failCount++
			continue
		}
		// 确认进程真死再删 marker（codex W2：避免进程还拿着显存就删 marker）。
		if !waitProcGone(ctx, pid, 2*time.Second) {
			log.GetLogger(ctx).Warnf("memfill pid[%s] still alive 2s after TERM (cudaFree slow?); marker kept", pid)
			failCount++
		}
	}
	if failCount > 0 {
		return fmt.Errorf("recover memfill incomplete: %d pid(s) could not be killed; marker kept for retry", failCount)
	}
	return removeMarker(uid)
}

// waitProcGone 轮询确认 pid 已退出（true=已退出）。最多等 timeout。
func waitProcGone(ctx context.Context, pid string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, _ := runHostShell(ctx, fmt.Sprintf("kill -0 %s 2>/dev/null && echo alive || echo gone", pid))
		if strings.TrimSpace(out) == "gone" {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// procStartTime 读 /proc/<pid>/stat 第 22 字段（启动时间，clock ticks）作为 PID 身份指纹。
// PID 复用后 start time 会变，用它校验"现在这个 pid 还是当初记录的那个进程"，防误杀
// （codex review Critical C2：仅凭 PID+comm 不足以可靠识别，崩后 PID 复用会误杀无关进程）。
func procStartTime(ctx context.Context, pid string) string {
	// awk 安全：pid 已在 kill 前用过，这里 pid 来自 marker（我们写的整数），不会含 shell 元字符。
	out, _ := runHostShell(ctx, fmt.Sprintf("awk '{print $22}' /proc/%s/stat 2>/dev/null", pid))
	return strings.TrimSpace(out)
}

// killHostPid 给宿主 pid 发 signal（TERM/KILL）。pid 不含 shell 元字符（来自 marker 整数）。
func killHostPid(ctx context.Context, pid, sig string) error {
	_, err := runHostShell(ctx, fmt.Sprintf("kill -%s %s 2>/dev/null", sig, pid))
	return err
}

// pidWithStarts 把纯 pid 列表转成 "pid:starttime" 列表，供 recover 做 PID 身份校验（C2）。
// 启动时间取不到时退化为纯 pid（recover 会走兼容分支，只校验 comm）。
func pidWithStarts(ctx context.Context, pids []string) []string {
	out := make([]string, 0, len(pids))
	for _, p := range pids {
		st := procStartTime(ctx, p)
		if st == "" {
			out = append(out, p)
		} else {
			out = append(out, p+":"+st)
		}
	}
	return out
}

// ============================ fault: reset (卡复位) ============================

func injectReset(ctx context.Context, args []string) error {
	targetIds, err := resolveTargetIds(ctx, args[0])
	if err != nil {
		return err
	}
	// reset 优雅降级前置：检测目标卡是否有在跑的算力进程；有则拒绝（reset 会杀推理进程）。
	for _, id := range targetIds {
		if busy, why := cardHasComputeApp(ctx, id); busy {
			return fmt.Errorf(
				"[reset] 拒绝复位 ppu[%s]: 该卡有活跃算力进程在跑（%s），reset 会中断线上推理/计算。\n"+
					"reset 是高危故障，本卡当前正在服务推理。如确需复位，请先在 ops 侧确认/排空该卡工作负载，再注入 reset。",
				id, why)
		}
	}
	var firstErr error
	for _, id := range targetIds {
		cmd := ppuCmd(fmt.Sprintf("-r -i %s", id))
		out, err := runPpu(ctx, cmd)
		if err != nil {
			// reset 错误降级：常见是因为卡忙/固件不允许在用中 reset。
			if msg, ok := degradeResetError(id, out); ok {
				log.GetLogger(ctx).Warnf("reset ppu[%s] error: %s", id, msg)
				if firstErr == nil {
					firstErr = fmt.Errorf("%s", msg)
				}
			} else {
				log.GetLogger(ctx).Warnf("reset ppu[%s] error: %s; out: %s", id, err, out)
				if firstErr == nil {
					firstErr = fmt.Errorf("reset ppu[%s] error: %s", id, strings.TrimSpace(out))
				}
			}
			continue
		}
	}
	return firstErr
}

// cardHasComputeApp 检测某卡是否有活跃算力进程（用 --query-compute-apps）。
// 返回 busy=true 时 why 描述障碍（进程名/pid）。由于 reset 是高危故障，本函数
// 必须 fail-closed：查询失败/不可判时返回 busy=true 且 fail=true，让调用方拒绝 reset，
// 而非把"未知是否忙"当成"安全"去 reset（codex review Critical: 原实现查询失败时返回
// busy=false 即 fail-open，会让 reset 误杀线上推理）。
func cardHasComputeApp(ctx context.Context, id string) (busy bool, why string) {
	out, err := runPpu(ctx, ppuCmd("--query-compute-apps=ppu_uuid,used_memory", "--format=csv,noheader", "-i", id))
	if err != nil {
		// 查询失败视为"该卡忙/状态未知"，拒绝 reset（fail-closed）。
		return true, fmt.Sprintf("无法确认该卡算力占用状态（--query-compute-apps 失败，不应在不确定时复位），raw=%q", strings.TrimSpace(out))
	}
	// -i N 的 compute-apps 输出若有行，说明该卡有算力进程。
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			return true, fmt.Sprintf("有算力进程占用（query-compute-apps 返回: %s）", strings.TrimSpace(ln))
		}
	}
	return false, ""
}

// degradeResetError 把 reset 的 ppu-smi 错误翻译成明确提示。
func degradeResetError(id, rawOut string) (string, bool) {
	low := strings.ToLower(rawOut)
	switch {
	case strings.Contains(low, "in use") || strings.Contains(low, "currently in use"):
		return fmt.Sprintf("[reset] ppu[%s] 复位失败：卡当前在用（有活跃算力上下文），驱动不允许在用中 reset。先排空该卡工作负载。根因: %q", id, strings.TrimSpace(rawOut)), true
	case strings.Contains(low, "not support") || strings.Contains(low, "not available"):
		return fmt.Sprintf("[reset] ppu[%s] 复位失败：该卡型/驱动不支持热 reset。根因: %q", id, strings.TrimSpace(rawOut)), true
	case strings.Contains(low, "permission"):
		return fmt.Sprintf("[reset] ppu[%s] 复位失败：权限不足。需 root + 特权。根因: %q", id, strings.TrimSpace(rawOut)), true
	}
	return "", false
}

func validatorTargetIds(ctx context.Context, args []string) error {
	if err := ensurePpuSmu(ctx); err != nil {
		return err
	}
	if _, err := resolveTargetIds(ctx, args[0]); err != nil {
		return err
	}
	return nil
}

// ============================ fault: clock (锁频降速) ============================
//
// 注入：lock PPU 计算时钟(CU)到指定 MHz，制造降速。
//   ppu-smi -lpc <minMHz,maxMHz> -i <id>
// Recover：reset 到默认 -rpc。本卡默认值可在 -q -d CLOCK 的 "Applications Clocks" 看到，
// 但 -rpc/reset-applications-clocks 更可靠，直接用。

func validatorClock(ctx context.Context, args []string) error {
	if err := ensurePpuSmu(ctx); err != nil {
		return err
	}
	if _, err := resolveTargetIds(ctx, args[0]); err != nil {
		return err
	}
	mhz, err := strconv.Atoi(args[1])
	if err != nil || mhz <= 0 {
		return fmt.Errorf("\"clock\"[%s] must be a positive int (MHz)", args[1])
	}
	return nil
}

// injectClock args: [targetIds, uid, clockMHz]
// 先抓每卡注入前的应用时钟存 marker（best-effort），再逐卡 -lpc 锁频。
// Recover 用 -rpc 复位（PPU 的 lock-clocks 默认复位=应用默认时钟，最可靠）。
func injectClock(ctx context.Context, args []string) error {
	targetIds, uid, mhz, err := parseClockArgs(ctx, args)
	if err != nil {
		return err
	}
	// clock 用 -rpc 恢复（reset 到默认应用时钟），不依赖注入前原值，故不抓 app_clocks
	// （codex M2：避免捕获从不读取的"死状态"）。marker 仅记录 ID 列表供 recover 逐卡 -rpc。
	if err := writeMarker(uid, strings.Join(targetIds, ",")); err != nil {
		return err
	}
	for _, id := range targetIds {
		cmd := ppuCmd(fmt.Sprintf("-lpc %d,%d -i %s", mhz, mhz, id))
		if out, err := runPpu(ctx, cmd); err != nil {
			return fmt.Errorf("lock clock for ppu[%s] error: %s; out: %s", id, err, out)
		}
	}
	return nil
}

func recoverClock(ctx context.Context, args []string) error {
	uid := args[0]
	idsStr, err := readMarker(uid)
	if err != nil {
		return err
	}
	var failCount int
	for _, id := range strings.Split(idsStr, ",") {
		if id == "" {
			continue
		}
		// -rpc 复位 PPU 锁频到默认应用时钟。
		cmd := ppuCmd(fmt.Sprintf("-rpc -i %s", id))
		if out, err := runPpu(ctx, cmd); err != nil {
			log.GetLogger(ctx).Warnf("reset clock for ppu[%s] error: %s; out: %s", id, err, out)
			failCount++
		}
	}
	if failCount > 0 {
		return fmt.Errorf("recover clock incomplete: %d card(s) failed to reset; marker kept for retry", failCount)
	}
	return removeMarker(uid)
}

// ============================ fault: power (降功率) ============================
//
// 注入：set power-limit 到给定瓦数（降功率→降性能）。范围 [Min,Max]，dev 节点 250-400W。
//   ppu-smi -pl <watts> -i <id>
// Recover：读出 Default Power Limit 设回（绝对值，不依赖硬编码）。

func validatorPower(ctx context.Context, args []string) error {
	if err := ensurePpuSmu(ctx); err != nil {
		return err
	}
	if _, err := resolveTargetIds(ctx, args[0]); err != nil {
		return err
	}
	w, err := strconv.Atoi(args[1])
	if err != nil || w <= 0 {
		return fmt.Errorf("\"power\"[%s] must be a positive int (watts)", args[1])
	}
	return nil
}

// injectPower args: [targetIds, uid, watts]
// 先抓每卡注入前的 power.limit 存进 marker，再逐卡设新值。Recover 设回原值（而非
// "默认限制"——某些卡当前限制可能≠默认限制，存原值才"恢复到位"）。
func injectPower(ctx context.Context, args []string) error {
	targetIds, uid, watts, err := parsePowerArgs(ctx, args)
	if err != nil {
		return err
	}
	state, err := captureState(ctx, targetIds, "power")
	if err != nil {
		return fmt.Errorf("capture pre-inject power state error: %s", err)
	}
	if err := writeStateMarker(uid, &markerRecord{IDs: targetIds, State: state}); err != nil {
		return err
	}
	for _, id := range targetIds {
		cmd := ppuCmd(fmt.Sprintf("-pl %d -i %s", watts, id))
		if out, err := runPpu(ctx, cmd); err != nil {
			return fmt.Errorf("set power-limit for ppu[%s] error: %s; out: %s", id, err, out)
		}
	}
	return nil
}

func recoverPower(ctx context.Context, args []string) error {
	uid := args[0]
	rec, err := readStateMarker(uid)
	if err != nil {
		return err
	}
	var failCount int
	for _, id := range rec.IDs {
		orig := ""
		if rec.State[id] != nil {
			orig = rec.State[id].Power
		}
		if orig == "" {
			// 旧 marker 缺原值，回退到查询 Default Power Limit（尽力而为）。
			def, derr := queryDefaultPower(ctx, id)
			if derr != nil {
				log.GetLogger(ctx).Warnf("no orig power and cannot query default for ppu[%s]: %s", id, derr)
				failCount++
				continue
			}
			orig = strconv.Itoa(def)
		}
		cmd := ppuCmd(fmt.Sprintf("-pl %s -i %s", orig, id))
		if out, err := runPpu(ctx, cmd); err != nil {
			log.GetLogger(ctx).Warnf("reset power for ppu[%s] to %s error: %s; out: %s", id, orig, err, out)
			failCount++
		}
	}
	if failCount > 0 {
		return fmt.Errorf("recover power incomplete: %d card(s) failed to reset; marker kept for retry", failCount)
	}
	return removeMarker(uid)
}

// queryDefaultPower 从 -q -d POWER 解析 "Default Power Limit : 400.00 W"。
func queryDefaultPower(ctx context.Context, id string) (int, error) {
	out, err := runPpu(ctx, ppuCmd("-q", "-d", "POWER", "-i", id))
	if err != nil {
		return 0, err
	}
	re := regexp.MustCompile(`(?m)^\s*Default\s+Power\s+Limit\s*:\s*([\d.]+)\s*W`)
	m := re.FindStringSubmatch(out)
	if len(m) < 2 {
		return 0, fmt.Errorf("cannot parse Default Power Limit from: %s", out)
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	return int(math.Round(f)), nil
}

// ============================ fault: compute-mode (拒绝算) ============================
//
// 注入：set compute-mode = PROHIBITED(2)，新算力进程无法在该卡跑（已跑的不受影响）。
//   ppu-smi -c 2 -i <id>
// Recover：设回 DEFAULT(0)。

func validatorComputeMode(ctx context.Context, args []string) error {
	if err := ensurePpuSmu(ctx); err != nil {
		return err
	}
	if _, err := resolveTargetIds(ctx, args[0]); err != nil {
		return err
	}
	mode := args[1]
	switch mode {
	case ComputeModeDefault, ComputeModeExclusiveProc, ComputeModeProhibited:
	default:
		return fmt.Errorf("\"mode\"[%s] must be one of %s/%s/%s", mode, ComputeModeDefault, ComputeModeExclusiveProc, ComputeModeProhibited)
	}
	return nil
}

// injectComputeMode args: [targetIds, uid, mode]
// 先抓每卡注入前的 compute_mode 存进 marker，再逐卡设新值。Recover 设回原值
// （而非硬编码 DEFAULT——原值可能是 EXCLUSIVE_PROCESS，硬编码 0 会改变状态）。
func injectComputeMode(ctx context.Context, args []string) error {
	targetIds, uid, mode, err := parseComputeModeArgs(ctx, args)
	if err != nil {
		return err
	}
	state, err := captureState(ctx, targetIds, "compute_mode")
	if err != nil {
		return fmt.Errorf("capture pre-inject compute_mode state error: %s", err)
	}
	if err := writeStateMarker(uid, &markerRecord{IDs: targetIds, State: state}); err != nil {
		return err
	}
	for _, id := range targetIds {
		cmd := ppuCmd(fmt.Sprintf("-c %s -i %s", mode, id))
		if out, err := runPpu(ctx, cmd); err != nil {
			return fmt.Errorf("set compute-mode for ppu[%s] error: %s; out: %s", id, err, out)
		}
	}
	return nil
}

func recoverComputeMode(ctx context.Context, args []string) error {
	uid := args[0]
	rec, err := readStateMarker(uid)
	if err != nil {
		return err
	}
	var failCount int
	for _, id := range rec.IDs {
		origCode := ComputeModeDefault
		if rec.State[id] != nil {
			origCode = computeModeTextToCode(rec.State[id].ComputeMode)
		}
		cmd := ppuCmd(fmt.Sprintf("-c %s -i %s", origCode, id))
		if out, err := runPpu(ctx, cmd); err != nil {
			log.GetLogger(ctx).Warnf("reset compute-mode for ppu[%s] to %s error: %s; out: %s", id, origCode, err, out)
			failCount++
		}
	}
	if failCount > 0 {
		return fmt.Errorf("recover compute-mode incomplete: %d card(s) failed to reset; marker kept for retry", failCount)
	}
	return removeMarker(uid)
}

// ============================ fault: appclocks (应用时钟降速) ============================
//
// 注入：set applications-clocks <memMHz,cuMHz>，把该卡"跑应用时"的时钟钉到指定值
// （与 -lpc 锁频不同：-ac 是应用时钟，可被 -lpc 覆盖；-ac 语义更接近 nvidia-smi 的 -ac）。
//   ppu-smi -ac <mem,cu> -i <id>
// Recover：先抓注入前的应用时钟存 marker，恢复时 -ac <原mem,原cu> 设回原值（恢复到位）；
//   若原值采集失败，回退 -rac（reset applications clocks 到默认）。

func validatorAppClocks(ctx context.Context, args []string) error {
	if err := ensurePpuSmu(ctx); err != nil {
		return err
	}
	if _, err := resolveTargetIds(ctx, args[0]); err != nil {
		return err
	}
	parts := strings.SplitN(args[1], ",", 2)
	if len(parts) != 2 {
		return fmt.Errorf("\"mem,cu\"[%s] must be <memMHz>,<cuMHz> eg: 1800,800", args[1])
	}
	for _, p := range parts {
		if v, err := strconv.Atoi(strings.TrimSpace(p)); err != nil || v <= 0 {
			return fmt.Errorf("\"mem,cu\"[%s] must be positive ints (MHz)", args[1])
		}
	}
	return nil
}

// injectAppClocks args: [targetIds, uid, "mem,cu"]
func injectAppClocks(ctx context.Context, args []string) error {
	targetIds, uid, memStr, cuStr, err := parseAppClocksArgs(ctx, args)
	if err != nil {
		return err
	}
	state, cerr := captureState(ctx, targetIds, "app_clocks")
	if cerr != nil {
		log.GetLogger(ctx).Warnf("capture pre-inject app clocks (best-effort): %s", cerr)
		state = map[string]*markerState{}
		for _, id := range targetIds {
			state[id] = &markerState{}
		}
	}
	if err := writeStateMarker(uid, &markerRecord{IDs: targetIds, State: state}); err != nil {
		return err
	}
	for _, id := range targetIds {
		cmd := ppuCmd(fmt.Sprintf("-ac %s,%s -i %s", memStr, cuStr, id))
		if out, err := runPpu(ctx, cmd); err != nil {
			return fmt.Errorf("set applications-clocks for ppu[%s] error: %s; out: %s", id, err, out)
		}
	}
	return nil
}

func recoverAppClocks(ctx context.Context, args []string) error {
	uid := args[0]
	rec, err := readStateMarker(uid)
	if err != nil {
		return err
	}
	var failCount int
	for _, id := range rec.IDs {
		var cmd string
		// 优先用注入前抓到的原应用时钟设回；拿不到就 -rac 复位到默认。
		if rec.State[id] != nil && rec.State[id].AppMem != "" && rec.State[id].AppCU != "" {
			cmd = ppuCmd(fmt.Sprintf("-ac %s,%s -i %s", rec.State[id].AppMem, rec.State[id].AppCU, id))
		} else {
			cmd = ppuCmd(fmt.Sprintf("-rac -i %s", id))
		}
		if out, err := runPpu(ctx, cmd); err != nil {
			// -ac 设回原值若不支持（某些卡原值即默认，-ac 可能拒），回退 -rac。
			if !strings.Contains(cmd, " -rac ") {
				log.GetLogger(ctx).Warnf("restore app clocks for ppu[%s] via -ac failed (%s), retry -rac", id, out)
				cmd2 := ppuCmd(fmt.Sprintf("-rac -i %s", id))
				if out2, err2 := runPpu(ctx, cmd2); err2 != nil {
					log.GetLogger(ctx).Warnf("reset app clocks for ppu[%s] error: %s; out: %s", id, err2, out2)
					failCount++
				}
				continue
			}
			log.GetLogger(ctx).Warnf("reset app clocks for ppu[%s] error: %s; out: %s", id, err, out)
			failCount++
		}
	}
	if failCount > 0 {
		return fmt.Errorf("recover appclocks incomplete: %d card(s) failed to reset; marker kept for retry", failCount)
	}
	return removeMarker(uid)
}

// ============================ fault: memclock (锁显存时钟, ZW810E N/A) ============================
//
// 用 -lmc 锁显存时钟降速。实测 PPU-ZW810E memory clock 不可锁，注入返回诚实错误，
// 故本 fault 接口保留但该卡型 N/A；memfill 用内存压力曲线，不依赖 -lmc。

func validatorMemClock(ctx context.Context, args []string) error {
	if err := ensurePpuSmu(ctx); err != nil {
		return err
	}
	if _, err := resolveTargetIds(ctx, args[0]); err != nil {
		return err
	}
	mhz, err := strconv.Atoi(args[1])
	if err != nil || mhz <= 0 {
		return fmt.Errorf("\"mhz\"[%s] for memclock must be a positive int (locked memory clock MHz)", args[1])
	}
	return nil
}

// injectMemClock args: [targetIds, uid, mhz]
func injectMemClock(ctx context.Context, args []string) error {
	targetIds, uid, mhz, err := parseMemClockArgs(ctx, args)
	if err != nil {
		return err
	}
	if err := writeMarker(uid, strings.Join(targetIds, ",")); err != nil {
		return err
	}
	for _, id := range targetIds {
		cmd := ppuCmd(fmt.Sprintf("-lmc %d,%d -i %s", mhz, mhz, id))
		if out, err := runPpu(ctx, cmd); err != nil {
			if strings.Contains(out, "not available") || strings.Contains(out, "invalid") {
				return fmt.Errorf("lock memory clock for ppu[%s] not supported on this device (PPU-ZW810E memory clock is fixed); memclock is N/A here. detail: %s", id, out)
			}
			return fmt.Errorf("lock memory clock for ppu[%s] error: %s; out: %s", id, err, out)
		}
	}
	return nil
}

func recoverMemClock(ctx context.Context, args []string) error {
	uid := args[0]
	idsStr, err := readMarker(uid)
	if err != nil {
		return err
	}
	var failCount int
	for _, id := range strings.Split(idsStr, ",") {
		if id == "" {
			continue
		}
		cmd := ppuCmd(fmt.Sprintf("-rmc -i %s", id))
		out, rerr := runPpu(ctx, cmd)
		// ZW810E 不支持锁显存，-rmc 报 not available 属预期（无操作要做），不计失败；
		// 其它错误计失败并保留 marker 以便重试（与其余 recover 一致，修复 codex M1）。
		if rerr != nil && !strings.Contains(out, "not available") {
			log.GetLogger(ctx).Warnf("reset memory clock for ppu[%s] error: %s; out: %s", id, rerr, out)
			failCount++
		}
	}
	if failCount > 0 {
		return fmt.Errorf("recover memclock incomplete: %d card(s) failed; marker kept for retry", failCount)
	}
	return removeMarker(uid)
}

func parseMemClockArgs(ctx context.Context, args []string) (ids []string, uid string, mhz int, err error) {
	ids, err = resolveTargetIds(ctx, args[0])
	if err != nil {
		return
	}
	uid = args[1]
	if err = validateUid(uid); err != nil {
		return
	}
	mhz, err = strconv.Atoi(args[2])
	return
}

// ============================ fault: 开关类（virtmode/mig/mps/autoreset/overclock/ecc） ============================
//
// 每个 = 注入时把开关切到指定码值，recover 恢复注入前原值。注入前用 -q -i 文本解析
// 抓取每个开关的当前值存 marker（recover-to-original）。ECC 注入会注明"需 reset 生效"。
//
// 注入参数：args = [targetIds, uid, code]; code 取值与各开关的 ppu-smi 数字一致：
//   virtmode: 0(NONE)/2(VGPU); mig/mps/autoreset/overclock: 0(disable)/1(enable); ecc: 0/1

// toggleTextLabel 把 -q -i 输出里每个开关块的开头标签映射到一个稳定 key，
// 便于从注入后大的 -q 输出里用 \"(?m)^\s*<Label>\n\s*:\s*(.*)\" 形态抓当前值。
func toggleQueryLabel(fault string) string {
	switch fault {
	case FaultPPUVirtMode:
		return "Virtualization Mode"
	case FaultPPUMig:
		return "MIG Mode"
	case FaultPPUMps:
		return "MPS Mode"
	case FaultPPUAutoReset:
		return "Auto Reset"
	case FaultPPUOverclock:
		return "Overclocking Mode"
	case FaultPPUEcc:
		return "Current" // ECC 现值在 Ecc Mode -> Current : Enabled
	}
	return ""
}

// queryToggleState 抓某卡某开关的当前文本值。
// 三种形态：
//   - 单行："<Label> : <value>"  (MPS Mode / Auto Reset / Overclocking Mode / Virtualization Mode)
//   - 块 + Current："<SectionHeader>\n ... Current : <value>"  (MIG Mode / Ecc Mode)
func queryToggleState(ctx context.Context, fault, id string) (string, error) {
	out, err := runPpu(ctx, ppuCmd("-q", "-i", id))
	if err != nil {
		return "", fmt.Errorf("query %s state for ppu[%s] error: %s; out: %s", fault, id, err, out)
	}
	label := toggleQueryLabel(fault)
	if label == "" {
		return "", fmt.Errorf("no query label for fault %s", fault)
	}
	// 先按"<Label> : <value>"单行匹配（适用于多数开关）。
	re := regexp.MustCompile(fmt.Sprintf(`(?m)^\s*%s\s*:\s*(.+)$`, regexp.QuoteMeta(label)))
	if m := re.FindStringSubmatch(out); len(m) >= 2 {
		return strings.TrimSpace(m[1]), nil
	}
	// 退到块形态：定位 <SectionHeader> 块，在其中抓 "Current : <value>"。
	reBlock := regexp.MustCompile(fmt.Sprintf(`(?ms)%s\b.*?\bCurrent\s*:\s*(\w+)`, regexp.QuoteMeta(label)))
	if m := reBlock.FindStringSubmatch(out); len(m) >= 2 {
		return strings.TrimSpace(m[1]), nil
	}
	return "", fmt.Errorf("cannot parse %s current value for ppu[%s]", fault, id)
}

// toggleTextToCode 把 -q 读出来的文本值转回 ppu-smi 设值用的数字码。
func toggleTextToCode(fault, text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	switch fault {
	case FaultPPUVirtMode:
		if strings.Contains(t, "vgpu") || strings.Contains(t, "2") {
			return "2"
		}
		return "0"
	case FaultPPUMig, FaultPPUMps, FaultPPUAutoReset:
		if strings.Contains(t, "enable") || strings.Contains(t, "1") {
			return "1"
		}
		return "0"
	case FaultPPUOverclock:
		if strings.Contains(t, "ultra") || strings.Contains(t, "1") {
			return "1"
		}
		return "0"
	case FaultPPUEcc:
		if strings.Contains(t, "enable") || strings.Contains(t, "1") {
			return "1"
		}
		return "0"
	}
	return "0"
}

// validatorToggle 对基础开关 fault 校验 args[1]=code；对方向化子故障 code 已由方向固定，
// 但仍接受透传的 code（与方向一致的 code 视为合法，否则报错）。
func validatorToggle(ctx context.Context, fault string, args []string) error {
	if err := ensurePpuSmu(ctx); err != nil {
		return err
	}
	if _, err := resolveTargetIds(ctx, args[0]); err != nil {
		return err
	}
	base, _ := resolveToggleFault(fault)
	code := args[1]
	if d, ok := directionalToggle[fault]; ok {
		code = d.code // 方向故障用固定 code 校验，忽略入参
	}
	if _, ok := strconv.Atoi(code); ok != nil {
		return fmt.Errorf("\"code\"[%s] for %s must be an int", code, base)
	}
	if base == FaultPPUVirtMode {
		if code != "0" && code != "2" {
			return fmt.Errorf("\"code\"[%s] for %s must be 0(NONE) or 2(VGPU)", code, base)
		}
	} else if code != "0" && code != "1" {
		return fmt.Errorf("\"code\"[%s] for %s must be 0 or 1", code, base)
	}
	return nil
}

// injectToggle args: [targetIds, uid, code]（方向化 fault 忽略 code 用方向固定值）
func injectToggle(ctx context.Context, fault string, args []string) error {
	base, _ := resolveToggleFault(fault)
	spec, ok := toggleSpecs[base]
	if !ok {
		return fmt.Errorf("not a toggle fault: %s", fault)
	}
	targetIds, uid, inCode, err := parseToggleArgs(ctx, args)
	if err != nil {
		return err
	}
	code := inCode
	if d, ok := directionalToggle[fault]; ok {
		code = d.code
	}
	// 抓注入前原值存 marker（recover-to-original）。原值必须可靠捕获，否则 recover
	// 无法恢复到位（codex review Critical C4+W：原实现 best-effort 捕获，失败时注入仍继续，
	// recover 只能伪造 0，永久破坏生产配置）。这里改为 fail-fast：任一卡原值捕获失败则拒绝注入。
	state := map[string]*markerState{}
	for _, id := range targetIds {
		orig, qerr := queryToggleState(ctx, base, id)
		if qerr != nil {
			return fmt.Errorf("[toggle:%s] 拒绝注入：无法捕获 ppu[%s] 的注入前原值（%s），recover 将无法恢复到位。请在该卡可正常查询 %s 状态后再注入。", base, id, qerr, base)
		}
		if strings.TrimSpace(orig) == "" {
			return fmt.Errorf("[toggle:%s] 拒绝注入：ppu[%s] 的注入前 %s 原值为空，无法安全 recover。", base, id, base)
		}
		state[id] = &markerState{ComputeMode: orig}
	}
	if err := writeStateMarker(uid, &markerRecord{IDs: targetIds, State: state}); err != nil {
		return err
	}
	var partialErr error
	for _, id := range targetIds {
		cmd := ppuCmd(fmt.Sprintf("%s %s -i %s", spec.flag, code, id))
		if out, err := runPpu(ctx, cmd); err != nil {
			// 受限故障优雅降级：把 ppu-smi 原始错误翻译成明确可操作的中文提示。
			if msg, ok := degradeToggleError(base, code, id, out); ok {
				partialErr = combineErr(partialErr, fmt.Errorf(msg))
			} else {
				partialErr = combineErr(partialErr, fmt.Errorf("set %s=%s for ppu[%s] error: %s; out: %s", base, code, id, err, out))
			}
		}
	}
	if base == FaultPPUEcc && partialErr == nil {
		log.GetLogger(ctx).Warnf("ecc change for ppu[%s]: ECC toggle needs a reset/reboot to take effect (pending!=current)", strings.Join(targetIds, ","))
	}
	return partialErr
}

// combineErr 串接多个错误，便于逐卡部分失败时一次返回。
func combineErr(prev, cur error) error {
	if prev == nil {
		return cur
	}
	return fmt.Errorf("%s; %s", prev.Error(), cur.Error())
}

// degradeToggleError 把受限开关故障（mig/mps/ecc/virtmode）的 ppu-smi 原始报错翻译成
// 明确、可操作的中文根因提示。命中返回 (msg,true)，否则 (false) 交给通用错误路径。
func degradeToggleError(base, code, id, rawOut string) (string, bool) {
	low := strings.ToLower(rawOut)
	switch base {
	case FaultPPUMig:
		if strings.Contains(low, "in use") || strings.Contains(low, "currently in use") {
			return fmt.Sprintf(
				"[%s] 失败: MIG 模式切换要求目标卡无在跑的算力进程，但 ppu[%s] 当前有推理/计算上下文占用。\n"+
					"根因: ppu-smi: %q\n"+
					"建议: 1) 不能在承载在线推理的卡上切 MIG; 2) 若确需切换, 先停掉该卡上的 sglang 等推理进程, 再注入 mig。", base, id, strings.TrimSpace(rawOut)), true
		}
		if strings.Contains(low, "not support") || strings.Contains(low, "not available") {
			return fmt.Sprintf("[%s] 失败: 该 PPU 卡型不支持 MIG 模式。根因: %q", base, strings.TrimSpace(rawOut)), true
		}
	case FaultPPUMps:
		if strings.Contains(low, "permission") {
			return fmt.Sprintf(
				"[%s] 失败: 当前用户没有开启 MPS 的权限（ppu-smi 即便 root 也拒绝）。\n"+
					"根因: ppu-smi: %q\n"+
					"建议: MPS 使能通常要求 root + 设备无活跃 CUDA 上下文 + 驱动策略允许; 线上推理卡一般不允许开 MPS, 属预期拒绝。", base, strings.TrimSpace(rawOut)), true
		}
		if strings.Contains(low, "in use") || strings.Contains(low, "currently in use") {
			return fmt.Sprintf(
				"[%s] 失败: MPS 切换要求目标卡无活跃 CUDA 上下文。ppu[%s] 当前在跑推理。\n根因: %q\n建议: 先停该卡推理进程再注入 mps。", base, id, strings.TrimSpace(rawOut)), true
		}
	case FaultPPUEcc:
		if strings.Contains(low, "reboot") || strings.Contains(low, "reset") {
			return fmt.Sprintf(
				"[%s] 已提交（code=%s），但 ECC 状态切换需 reset/reboot 才生效（pending≠current）。\n"+
					"注意: ecc 故障默认不自动 reset(会复位推理卡); 如需生效需人工确认后再 reset ppu[%s]。", base, code, id), true
		}
	case FaultPPUVirtMode:
		if strings.Contains(low, "in use") {
			return fmt.Sprintf("[%s] 失败: 虚拟化模式切换要求目标卡无在跑进程。ppu[%s] 当前在跑推理。根因: %q", base, id, strings.TrimSpace(rawOut)), true
		}
	}
	return "", false
}

func recoverToggle(ctx context.Context, fault string, args []string) error {
	base, _ := resolveToggleFault(fault)
	spec, ok := toggleSpecs[base]
	if !ok {
		return fmt.Errorf("not a toggle fault: %s", fault)
	}
	uid := args[0]
	rec, err := readStateMarker(uid)
	if err != nil {
		return err
	}
	var failCount int
	for _, id := range rec.IDs {
		// recover-to-original：必须用注入前抓到的原值恢复，绝不能在原值缺失时伪造 "0"
		// 去复位（codex review Critical C4：原实现 capture 失败时 recover 默认 "0"，会把
		// 原本 Overclock=Ultra / ECC=MIG=enabled / VGPU 等生产配置永久改成 0）。原值缺失
		// 时保留标记并报错，交人工确认，而非用默认值覆盖配置。
		var origCode string
		var haveOrig bool
		if rec.State[id] != nil && rec.State[id].ComputeMode != "" {
			origCode = toggleTextToCode(base, rec.State[id].ComputeMode)
			haveOrig = true
		}
		if !haveOrig {
			log.GetLogger(ctx).Warnf("recover %s for ppu[%s]: 注入前原值未捕获，拒绝用默认 0 覆盖（避免破坏生产配置）；标记保留交人工", fault, id)
			failCount++
			continue
		}
		cmd := ppuCmd(fmt.Sprintf("%s %s -i %s", spec.flag, origCode, id))
		if out, err := runPpu(ctx, cmd); err != nil {
			log.GetLogger(ctx).Warnf("reset %s to %s for ppu[%s] error: %s; out: %s", fault, origCode, id, err, out)
			failCount++
		}
	}
	if failCount > 0 {
		return fmt.Errorf("recover %s incomplete: %d card(s) failed; marker kept for retry", fault, failCount)
	}
	return removeMarker(uid)
}

func parseToggleArgs(ctx context.Context, args []string) (ids []string, uid, code string, err error) {
	ids, err = resolveTargetIds(ctx, args[0])
	if err != nil {
		return
	}
	uid = args[1]
	if err = validateUid(uid); err != nil {
		return
	}
	code = args[2]
	return
}

// ============================ args parsers ============================

func parseAppClocksArgs(ctx context.Context, args []string) (ids []string, uid, memStr, cuStr string, err error) {
	ids, err = resolveTargetIds(ctx, args[0])
	if err != nil {
		return
	}
	uid = args[1]
	if err = validateUid(uid); err != nil {
		return
	}
	parts := strings.SplitN(args[2], ",", 2)
	if len(parts) != 2 {
		err = fmt.Errorf("\"mem,cu\"[%s] must be <memMHz>,<cuMHz>", args[2])
		return
	}
	memStr = strings.TrimSpace(parts[0])
	cuStr = strings.TrimSpace(parts[1])
	return
}

func parseBurnArgs(ctx context.Context, args []string) (ids []string, uid string, percent int, err error) {
	ids, err = resolveTargetIds(ctx, args[0])
	if err != nil {
		return
	}
	uid = args[1]
	if err = validateUid(uid); err != nil {
		return
	}
	percent, _ = strconv.Atoi(args[2])
	if percent == 0 {
		percent = 100
	}
	return
}

func parseMemFillArgs(ctx context.Context, args []string) (ids []string, uid string, mb int, err error) {
	ids, err = resolveTargetIds(ctx, args[0])
	if err != nil {
		return
	}
	uid = args[1]
	if err = validateUid(uid); err != nil {
		return
	}
	mb, err = strconv.Atoi(args[2])
	if err != nil {
		return
	}
	if mb <= 0 {
		mb = 256
	}
	return
}

func parseClockArgs(ctx context.Context, args []string) (ids []string, uid string, mhz int, err error) {
	ids, err = resolveTargetIds(ctx, args[0])
	if err != nil {
		return
	}
	uid = args[1]
	if err = validateUid(uid); err != nil {
		return
	}
	mhz, err = strconv.Atoi(args[2])
	return
}

func parsePowerArgs(ctx context.Context, args []string) (ids []string, uid string, watts int, err error) {
	ids, err = resolveTargetIds(ctx, args[0])
	if err != nil {
		return
	}
	uid = args[1]
	if err = validateUid(uid); err != nil {
		return
	}
	watts, err = strconv.Atoi(args[2])
	return
}

func parseComputeModeArgs(ctx context.Context, args []string) (ids []string, uid string, mode string, err error) {
	ids, err = resolveTargetIds(ctx, args[0])
	if err != nil {
		return
	}
	uid = args[1]
	if err = validateUid(uid); err != nil {
		return
	}
	mode = args[2]
	return
}

// ============================ runtime marker file ============================
//
// 用文件记录注入时的目标卡列表 + 注入前的原始状态（power.limit / compute_mode /
// clocks），Recover 时读回并恢复成原值（而非硬编码默认值），保证"恢复到位"且幂等、
// 不依赖 DB。路径 /tmp/chaosmeta_ppu_<uid>.marker。
//
// marker 内容：一行 JSON：{"ids":["0","1"],"state":{"0":{...},"1":{...}}}
// 旧格式（纯 "0,1" 逗号列表）仍兼容：readMarkerIdList 解析旧格式。

func markerPath(uid string) string {
	return fmt.Sprintf("%s/chaosmeta_ppu_%s.marker", os.TempDir(), uid)
}

// validateUid 对 uid 做严格白名单校验（codex review Critical C5 防御）。uid 一路被拼进
// 宿主机 shell 命令（runHostShell 的 fmt.Sprintf）和 marker 文件名，校验可杜绝含 `; / ..`
// 等元字符导致的宿主机命令注入或 marker 路径穿越。
//
// 关键：invalid uid 必须**拒绝操作**（返回 error），绝不能映射成共享的 "invalid" 文件名——
// 否则两个脏 uid 请求会互相覆盖/删 marker，甚至 recover 掉对方的进程/设备状态
// （codex 闭环复验：原 safeUid 把脏 uid 别名为 "invalid" 引发的 collision Critical）。
var uidRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,128}$`)

// validateUid 在需要把 uid 用于 shell/文件之前校验；非法返回 error，调用方应直接返回该错误
// 而非继续注入/恢复。
func validateUid(uid string) error {
	if uidRe.MatchString(uid) {
		return nil
	}
	return fmt.Errorf("invalid uid %q: 必须匹配 ^[A-Za-z0-9_.\\-]{1,128}$（拒绝执行以防宿主机命令注入或 marker 碰撞）", uid)
}

// uidSafePath 校验 uid 后返回 marker 路径；非法则返回 error，调用方据此直接失败而非落盘。
func uidSafePath(uid string) (string, error) {
	if err := validateUid(uid); err != nil {
		return "", err
	}
	return markerPath(uid), nil
}

func writeMarker(uid, ids string) error {
	p, err := uidSafePath(uid)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(ids), 0600)
}

func readMarker(uid string) (string, error) {
	p, err := uidSafePath(uid)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read marker for uid[%s] error: %s (was inject run?)", uid, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func removeMarker(uid string) error {
	p, err := uidSafePath(uid)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(p)
}

// ---- 状态感知 marker（新格式 JSON）----

// markerState 记录某卡注入前的可恢复原始状态。字段为空表示该 fault 不关心。
type markerState struct {
	Power       string `json:"power,omitempty"`        // 注入前 power.limit（W，无单位），如 "400"
	ComputeMode string `json:"compute_mode,omitempty"` // 注入前 compute_mode 文本，如 "Default"/"Prohibited"
	// appclocks 注入才用：注入前 applications clocks (mem,cu MHz 无单位)，如 "1800,1700"
	AppMem string `json:"app_mem,omitempty"`
	AppCU  string `json:"app_cu,omitempty"`
}

type markerRecord struct {
	IDs   []string                `json:"ids"`
	State map[string]*markerState `json:"state"`
}

func writeStateMarker(uid string, rec *markerRecord) error {
	p, err := uidSafePath(uid)
	if err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0600)
}

func readStateMarker(uid string) (*markerRecord, error) {
	p, err := uidSafePath(uid)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read marker for uid[%s] error: %s (was inject run?)", uid, err)
	}
	var rec markerRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("parse marker for uid[%s] error: %s; raw: %s", uid, err, string(b))
	}
	return &rec, nil
}

// captureState 抓取一组卡的原始状态（只填关心的字段：填哪些看 fields）。
// fields: "power","compute_mode","app_clocks" 的子集。
func captureState(ctx context.Context, ids []string, fields ...string) (map[string]*markerState, error) {
	want := map[string]bool{}
	for _, f := range fields {
		want[f] = true
	}
	state := map[string]*markerState{}
	for _, id := range ids {
		ms := &markerState{}
		if want["power"] {
			v, err := queryPpuCsv(ctx, "power.limit", id)
			if err != nil {
				return nil, err
			}
			ms.Power = v
		}
		if want["compute_mode"] {
			v, err := queryPpuCsv(ctx, "compute_mode", id)
			if err != nil {
				return nil, err
			}
			ms.ComputeMode = v
		}
		if want["app_clocks"] {
			// app clocks 没有 csv 字段，从 -q -d CLOCK 文本里解析 Applications Clocks 的 CU/Mem。
			mem, cu, err := queryAppClocks(ctx, id)
			if err != nil {
				return nil, err
			}
			ms.AppMem = mem
			ms.AppCU = cu
		}
		state[id] = ms
	}
	return state, nil
}

// computeModeTextToCode 把 compute_mode 文本（"Default"/"Prohibited"/...）转回 -c 用的数字。
// 拿不到就返回 ComputeModeDefault。
func computeModeTextToCode(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.Contains(t, "prohibit"):
		return ComputeModeProhibited
	case strings.Contains(t, "exclusive"):
		return ComputeModeExclusiveProc
	default:
		return ComputeModeDefault
	}
}
