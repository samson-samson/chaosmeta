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

package injector

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/log"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/storage"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/utils/cmdexec"
)

// staleScanInterval is how often the periodic (post-startup) sweep runs.
const staleScanInterval = 5 * time.Minute

// RunStaleScanLoop runs an immediate startup scan, then a periodic sweep.
// Safe to run as a goroutine; never returns until ctx is done.
//
// D6 design contract (see docs/design/fault-injection-enhancement.md §2.3.1):
// This scan is the ONLY crash-recovery backstop. Its iron rule is to NEVER sweep an
// experiment whose auto-recover timer is still alive — that would prematurely kill every
// in-window timed experiment on every daemon restart (catastrophic blast radius).
// It only acts on:
//   - success + orphan timer lost AND past deadline -> true residue -> recover
//   - success + orphan timer lost AND before deadline -> re-fork the timer (continue, do NOT recover)
//   - detached orphan `sleep N; chaosmetad recover <uid>` processes whose uid is already destroyed -> reap
//
// It deliberately leaves alone:
//   - success + timer still alive (healthy, resident)
//   - any record with empty orphan tracking columns (legacy) and undetermined deadline -> mark "manual verify" via metrics/log, do NOT auto-recover
//   - error / paused states -> list for manual confirmation, do NOT auto-recover (D5 fix lets an explicit stop meaningfully recover error records)
func RunStaleScanLoop(ctx context.Context) {
	logger := log.GetLogger(ctx)
	logger.Info("stale-recovery scan: running startup sweep")
	scanStaleExperiments(ctx)
	scanDetachedOrphans(ctx)

	t := time.NewTicker(staleScanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("stale-recovery scan: stopping")
			return
		case <-t.C:
			scanStaleExperiments(ctx)
			scanDetachedOrphans(ctx)
		}
	}
}

// scanStaleExperiments enforces the §2.3.1 safety matrix over DB records.
func scanStaleExperiments(ctx context.Context) {
	logger := log.GetLogger(ctx)
	db, err := storage.GetExperimentStore()
	if err != nil {
		logger.Warnf("stale scan: connect db error: %s", err.Error())
		return
	}

	// Only inspect states that could be resident. Destroyed/created are skipped.
	exps, err := db.ListByStatus(utils.StatusSuccess, utils.StatusPaused, utils.StatusError)
	if err != nil {
		logger.Warnf("stale scan: list error: %s", err.Error())
		return
	}

	now := time.Now().Unix()
	for _, exp := range exps {
		switch exp.Status {
		case utils.StatusSuccess:
			handleResidentSuccess(ctx, exp, now)
		case utils.StatusError:
			// Fault may be resident. Do NOT auto-recover from a background sweep (ambiguous);
			// surface for manual confirmation. An explicit stop / recover command will route through
			// the D5-fixed BaseInjector.Recover and clean it for real.
			logger.Warnf("stale scan: uid[%s] in error state, possible resident fault — manual confirmation required", exp.Uid)
		case utils.StatusPaused:
			// Cross-restart paused state cannot be faithfully reconstructed (original SIGSTOP'd
			// procs are unknown). Surface for manual confirmation, do not touch.
			logger.Warnf("stale scan: uid[%s] paused at restart — manual confirmation required", exp.Uid)
		}
	}
}

// handleResidentSuccess implements the safety matrix for a single success record.
func handleResidentSuccess(ctx context.Context, exp *storage.Experiment, now int64) {
	logger := log.GetLogger(ctx)

	// Case A: a tracked orphan PID exists and is still our sleep-recover process.
	// This is a healthy, in-window resident experiment. NEVER sweep.
	if exp.OrphanPid > 0 && isAliveSleepRecover(exp.OrphanPid, exp.Uid) {
		logger.Debugf("stale scan: uid[%s] timer alive (pid=%d), leaving resident", exp.Uid, exp.OrphanPid)
		return
	}

	// Case B: timer lost (no pid, pid dead, or wrong process) — distinguish by deadline.
	hadDeadline := exp.RecoverDeadline > 0
	if !hadDeadline {
		// Legacy record with no tracking info and no deadline: we cannot tell "timer alive and
		// just hasn't fired" from "timer lost and residue remains". Forcing recover would risk
		// killing a legitimately in-window experiment. FAIL SAFE: do not touch, surface it.
		logger.Warnf("stale scan: uid[%s] success with no timer tracking (legacy) — leaving untouched, manual confirmation required", exp.Uid)
		return
	}

	if now < exp.RecoverDeadline {
		// Timer lost but its window hasn't elapsed yet. The forked orphan died early (e.g. OOM-killer
		// hit it, or the machine rebooted and the detached process didn't survive). Re-fork a fresh
		// timer for the remaining duration so the experiment continues as intended. Do NOT recover.
		remaining := exp.RecoverDeadline - now
		if remaining < 1 {
			remaining = 1
		}
		if _, _, derr := restartOrphanTimer(ctx, exp.Uid, remaining); derr != nil {
			logger.Warnf("stale scan: uid[%s] re-fork timer failed (%s) — recovering to avoid silent residue", exp.Uid, derr.Error())
			// Timer re-fork failed AND we're still before deadline; a lost timer with no way to
			// restart is unsafe to leave running. Recover now to be safe.
			recoverStaleResidue(ctx, exp.Uid, "timer refork failed before deadline")
		}
		return
	}

	// Case C: past deadline, still success, timer not alive = genuine residue from a crash
	// before/around the auto-recover. This is the D6 backstop's real target. Recover it.
	recoverStaleResidue(ctx, exp.Uid, "timer lost and past deadline")
}

func recoverStaleResidue(ctx context.Context, uid, reason string) {
	logger := log.GetLogger(ctx)
	logger.Warnf("stale scan: uid[%s] true residue (%s) — recovering", uid, reason)
	code, msg := ProcessRecover(ctx, uid)
	if code != 0 {
		logger.Warnf("stale scan: recover uid[%s] failed code=%d msg=%s — leaving for manual recovery", uid, code, msg)
	} else {
		logger.Infof("stale scan: recovered residue uid[%s]", uid)
	}
}

// restartOrphanTimer re-forks a detached timer for the given remaining seconds and persists the new
// pid/deadline. Uses the cached global store.
func restartOrphanTimer(ctx context.Context, uid string, remaining int64) (int, int64, error) {
	db, err := storage.GetExperimentStore()
	if err != nil {
		return utils.NoPid, 0, err
	}
	pid, deadline, err := startOrphanTimer(ctx, uid, remaining)
	if err != nil {
		return utils.NoPid, 0, err
	}
	if err := db.UpdateOrphan(uid, pid, deadline); err != nil {
		return pid, deadline, err
	}
	return pid, deadline, nil
}

// startOrphanTimer forks the same detached sleep-recover process as ProcessInject does,
// without taking the inject path. Reused for the stale-scan "re-fork lost timer" case.
func startOrphanTimer(ctx context.Context, uid string, sleepSec int64) (int, int64, error) {
	return cmdexec.StartSleepRecoverWithPid(ctx, sleepSec, uid)
}

// scanDetachedOrphans walks /proc-like info for `sleep N; chaosmetad recover <uid>` processes whose
// uid is already destroyed and reaps them, preventing zombie accumulation across long runs.
// Implementation is conservative: it scans /proc/*/cmdline, matches the recover-cmd pattern, and
// signals only orphans that belong to a destroyed uid.
func scanDetachedOrphans(ctx context.Context) {
	logger := log.GetLogger(ctx)
	db, err := storage.GetExperimentStore()
	if err != nil {
		logger.Warnf("detached-orphan scan: connect db error: %s", err.Error())
		return
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		// /proc unavailable (non-Linux) — skip silently.
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil || pid <= 0 {
			continue
		}
		cmdline, cerr := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if cerr != nil {
			continue
		}
		cmd := strings.ReplaceAll(string(cmdline), "\x00", " ")
		uid := matchSleepRecoverCmd(cmd)
		if uid == "" {
			continue
		}
		// Only reap orphans whose experiment is already destroyed (timer should have self-completed).
		exp, gerr := db.GetByUid(uid)
		if gerr != nil || exp == nil {
			continue
		}
		if exp.Status == utils.StatusDestroyed {
			logger.Warnf("detached-orphan scan: reaping zombie timer pid=%d uid=%s (experiment already destroyed)", pid, uid)
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
}

// matchSleepRecoverCmd returns the uid embedded in a `sleep N; .../chaosmetad recover <uid>`
// command line, or "" if it doesn't match that pattern.
func matchSleepRecoverCmd(cmd string) string {
	// cmdline reassembled with spaces; match "<rootname> recover <uid>" tail.
	root := utils.RootName
	idx := strings.Index(cmd, root+" recover ")
	if idx < 0 {
		return ""
	}
	tail := cmd[idx+len(root)+len(" recover "):]
	tail = strings.TrimSpace(tail)
	// uid is the first token (may be followed by redirection ">> /tmp/...").
	if sp := strings.IndexAny(tail, " \t"); sp >= 0 {
		tail = tail[:sp]
	}
	if err := utils.IsValidUid(tail); err != nil {
		return ""
	}
	return tail
}

// isAliveSleepRecover checks whether pid is a live process whose command is our sleep-recover for uid.
func isAliveSleepRecover(pid int, uid string) bool {
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	cmd := strings.ReplaceAll(string(cmdline), "\x00", " ")
	matchedUid := matchSleepRecoverCmd(cmd)
	return matchedUid == uid
}
