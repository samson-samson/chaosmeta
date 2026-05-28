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
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/log"
)

func init() {
	injector.Register(TargetAI, FaultTokenDrop, func() injector.IInjector { return &TokenDropInjector{} })
}

// TokenDropInjector stands up the same reverse proxy as infer_latency, but
// configured to randomly drop a fraction of streamed events (SSE / NDJSON)
// on their way back to the client. The model server happily produces tokens;
// the client receives a shorter or corrupted-looking stream.
type TokenDropInjector struct {
	injector.BaseInjector
	Args    TokenDropArgs
	Runtime TokenDropRuntime
}

type TokenDropArgs struct {
	ListenAddr string  `json:"listen_addr"`
	Upstream   string  `json:"upstream"`
	DropRate   float64 `json:"drop_rate"`
	PathPrefix string  `json:"path_prefix,omitempty"`
	Seed       int64   `json:"seed,omitempty"`
}

type TokenDropRuntime struct {
	PID int `json:"pid,omitempty"`
}

func (i *TokenDropInjector) GetArgs() interface{}    { return &i.Args }
func (i *TokenDropInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *TokenDropInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.ListenAddr, "listen", "L", "",
		"proxy listen address, e.g. \"0.0.0.0:18081\" (required)")
	cmd.Flags().StringVarP(&i.Args.Upstream, "upstream", "u", "",
		"upstream model server, e.g. \"http://127.0.0.1:8000\" (required)")
	cmd.Flags().Float64VarP(&i.Args.DropRate, "rate", "r", 0,
		"probability each streamed event is dropped, in (0,1] (required)")
	cmd.Flags().StringVar(&i.Args.PathPrefix, "path-prefix", "",
		"only inject on requests whose path starts with this, e.g. \"/v1/\"")
	cmd.Flags().Int64Var(&i.Args.Seed, "seed", 0,
		"PRNG seed for deterministic drops (default: time-based)")
}

func (i *TokenDropInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.ListenAddr == "" {
		return fmt.Errorf("\"listen\" must be provided")
	}
	if _, _, err := splitHostPort(i.Args.ListenAddr); err != nil {
		return fmt.Errorf("\"listen\"[%s] invalid: %s", i.Args.ListenAddr, err.Error())
	}
	if _, err := url.ParseRequestURI(i.Args.Upstream); err != nil {
		return fmt.Errorf("\"upstream\"[%s] invalid: %s", i.Args.Upstream, err.Error())
	}
	if i.Args.DropRate <= 0 || i.Args.DropRate > 1 {
		return fmt.Errorf("\"rate\"[%v] must be in (0,1]", i.Args.DropRate)
	}
	cfg, err := buildProxyConfig(i.Args.Upstream, "0s", "0s", i.Args.PathPrefix, i.Args.DropRate, i.Args.Seed)
	if err != nil {
		return err
	}
	return cfg.Validate()
}

func (i *TokenDropInjector) Inject(ctx context.Context) error {
	logger := log.GetLogger(ctx)
	args := []string{
		"--uid", i.Info.Uid,
		"--listen", i.Args.ListenAddr,
		"--upstream", i.Args.Upstream,
		"--rate", strconv.FormatFloat(i.Args.DropRate, 'f', -1, 64),
		"--path-prefix", i.Args.PathPrefix,
		"--seed", strconv.FormatInt(i.Args.Seed, 10),
	}
	pid, err := activeStarter.Start(FaultTokenDrop, args)
	if err != nil {
		return fmt.Errorf("start ai-daemon: %w", err)
	}
	i.Runtime.PID = pid
	logger.Infof("token_drop daemon started pid=%d listen=%s upstream=%s rate=%v",
		pid, i.Args.ListenAddr, i.Args.Upstream, i.Args.DropRate)
	return nil
}

func (i *TokenDropInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	if i.Runtime.PID == 0 {
		removePidFile(i.Info.Uid)
		return nil
	}
	err := activeStarter.Stop(i.Runtime.PID)
	removePidFile(i.Info.Uid)
	i.Runtime.PID = 0
	return err
}
