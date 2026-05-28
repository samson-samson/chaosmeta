/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

// Package aidaemon is the long-running half of the AI proxy injectors. The
// inject CLI starts a chaosmetad subprocess in this mode; it runs the proxy
// until SIGTERM. This file lives in cmd/ rather than pkg/ because it owns
// process-level concerns (pidfile, signal handling).
package aidaemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector/ai/proxy"
)

// NewAIDaemonCommand returns the hidden `ai-daemon` subcommand. The first
// positional arg names the fault (infer_latency / token_drop); the rest are
// flag-style key/value pairs the injector passed via exec.
func NewAIDaemonCommand() *cobra.Command {
	var (
		uid        string
		listenAddr string
		upstream   string
		latency    string
		jitter     string
		pathPrefix string
		dropRate   float64
		seed       int64
	)
	cmd := &cobra.Command{
		Use:    "ai-daemon [fault]",
		Short:  "internal: long-running AI proxy daemon",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := proxy.Config{
				Upstream:      upstream,
				PathPrefix:    pathPrefix,
				TokenDropRate: dropRate,
				Seed:          seed,
			}
			if latency != "" {
				d, err := time.ParseDuration(latency)
				if err != nil {
					return fmt.Errorf("latency: %w", err)
				}
				cfg.Latency = d
			}
			if jitter != "" && jitter != "0s" && jitter != "0" {
				d, err := time.ParseDuration(jitter)
				if err != nil {
					return fmt.Errorf("jitter: %w", err)
				}
				cfg.Jitter = d
			}
			h, err := proxy.New(cfg)
			if err != nil {
				return err
			}
			srv := &http.Server{Addr: listenAddr, Handler: h}
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()
			if uid != "" {
				_ = writePid(uid, os.Getpid())
				defer removePidFile(uid)
			}
			go func() {
				<-ctx.Done()
				shutdownCtx, c := context.WithTimeout(context.Background(), 2*time.Second)
				defer c()
				_ = srv.Shutdown(shutdownCtx)
			}()
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&uid, "uid", "", "experiment uid")
	cmd.Flags().StringVar(&listenAddr, "listen", "", "listen address")
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream URL")
	cmd.Flags().StringVar(&latency, "latency", "0s", "request latency, Go duration")
	cmd.Flags().StringVar(&jitter, "jitter", "0s", "request jitter, Go duration")
	cmd.Flags().StringVar(&pathPrefix, "path-prefix", "", "path prefix filter")
	cmd.Flags().Float64Var(&dropRate, "rate", 0, "token drop rate")
	cmd.Flags().Int64Var(&seed, "seed", 0, "PRNG seed for drops")
	return cmd
}

func writePid(uid string, pid int) error {
	path := pidPath(uid)
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644)
}

func removePidFile(uid string) { _ = os.Remove(pidPath(uid)) }

func pidPath(uid string) string {
	return fmt.Sprintf("%s/chaosmeta_ai_%s.pid", os.TempDir(), uid)
}
