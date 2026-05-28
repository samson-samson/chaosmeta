/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package ai

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// daemonStarter abstracts the "fork a long-running proxy" side effect so unit
// tests can verify the injector wired arguments correctly without actually
// spawning a process. The real implementation re-execs chaosmetad with a
// hidden subcommand (`ai-daemon`).
type daemonStarter interface {
	Start(name string, args []string) (pid int, err error)
	Stop(pid int) error
}

// activeStarter is overridden by tests via withTestStarter.
var activeStarter daemonStarter = realStarter{}

func withTestStarter(s daemonStarter) func() {
	prev := activeStarter
	activeStarter = s
	return func() { activeStarter = prev }
}

// realStarter spawns `chaosmetad ai-daemon ...` detached.
type realStarter struct{}

func (realStarter) Start(name string, args []string) (int, error) {
	bin, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate chaosmetad binary: %w", err)
	}
	full := append([]string{"ai-daemon", name}, args...)
	cmd := exec.Command(bin, full...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	// Detach: new session so the daemon survives the CLI invocation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

func (realStarter) Stop(pid int) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// Already gone is fine.
		if strings.Contains(err.Error(), "process already finished") {
			return nil
		}
		return err
	}
	// Best-effort: give it a moment, then SIGKILL if still alive.
	for i := 0; i < 20; i++ {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = proc.Signal(syscall.SIGKILL)
	return nil
}

// daemonStateFile returns the path where a daemon writes its pidfile. The
// pidfile is informational; the authoritative pid is in the experiment runtime.
func daemonStateFile(uid string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("chaosmeta_ai_%s.pid", uid))
}

// writePid is used by the ai-daemon subcommand.
func writePid(uid string, pid int) error {
	return os.WriteFile(daemonStateFile(uid), []byte(strconv.Itoa(pid)), 0644)
}

// removePidFile silently best-effort.
func removePidFile(uid string) { _ = os.Remove(daemonStateFile(uid)) }

// drainContext blocks until ctx is done. Helper for the daemon main loop.
func drainContext(ctx context.Context) { <-ctx.Done() }
