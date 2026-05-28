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
	"time"

	"github.com/spf13/cobra"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector/ai/proxy"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/log"
)

func init() {
	injector.Register(TargetAI, FaultInferLatency, func() injector.IInjector { return &InferLatencyInjector{} })
}

// InferLatencyInjector stands up a reverse proxy that delays requests to a
// model server. Point your client at the proxy's listen address instead of
// the upstream and you get extra latency on every /v1/* request (configurable
// via path_prefix), simulating a slow inference backend.
type InferLatencyInjector struct {
	injector.BaseInjector
	Args    InferLatencyArgs
	Runtime InferLatencyRuntime
}

type InferLatencyArgs struct {
	ListenAddr string `json:"listen_addr"`
	Upstream   string `json:"upstream"`
	Latency    string `json:"latency"`
	Jitter     string `json:"jitter,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty"`
}

type InferLatencyRuntime struct {
	PID int `json:"pid,omitempty"`
}

func (i *InferLatencyInjector) GetArgs() interface{}    { return &i.Args }
func (i *InferLatencyInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *InferLatencyInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.ListenAddr, "listen", "L", "",
		"proxy listen address, e.g. \"0.0.0.0:18080\" (required)")
	cmd.Flags().StringVarP(&i.Args.Upstream, "upstream", "u", "",
		"upstream model server, e.g. \"http://127.0.0.1:8000\" (required)")
	cmd.Flags().StringVarP(&i.Args.Latency, "latency", "l", "",
		"per-request added latency as Go duration, e.g. \"500ms\" (required)")
	cmd.Flags().StringVarP(&i.Args.Jitter, "jitter", "j", "0s",
		"uniform extra latency in [0,jitter), Go duration, e.g. \"100ms\"")
	cmd.Flags().StringVar(&i.Args.PathPrefix, "path-prefix", "",
		"only inject on requests whose path starts with this, e.g. \"/v1/\"")
}

func (i *InferLatencyInjector) Validator(ctx context.Context) error {
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
	if i.Args.Latency == "" {
		return fmt.Errorf("\"latency\" must be provided")
	}
	if _, err := time.ParseDuration(i.Args.Latency); err != nil {
		return fmt.Errorf("\"latency\" invalid: %s", err.Error())
	}
	if i.Args.Jitter != "" {
		if _, err := time.ParseDuration(i.Args.Jitter); err != nil {
			return fmt.Errorf("\"jitter\" invalid: %s", err.Error())
		}
	}
	cfg, err := buildProxyConfig(i.Args.Upstream, i.Args.Latency, i.Args.Jitter, i.Args.PathPrefix, 0, 0)
	if err != nil {
		return err
	}
	return cfg.Validate()
}

func (i *InferLatencyInjector) Inject(ctx context.Context) error {
	logger := log.GetLogger(ctx)
	args := []string{
		"--uid", i.Info.Uid,
		"--listen", i.Args.ListenAddr,
		"--upstream", i.Args.Upstream,
		"--latency", i.Args.Latency,
		"--jitter", defaultIfEmpty(i.Args.Jitter, "0s"),
		"--path-prefix", i.Args.PathPrefix,
	}
	pid, err := activeStarter.Start(FaultInferLatency, args)
	if err != nil {
		return fmt.Errorf("start ai-daemon: %w", err)
	}
	i.Runtime.PID = pid
	logger.Infof("infer_latency daemon started pid=%d listen=%s upstream=%s",
		pid, i.Args.ListenAddr, i.Args.Upstream)
	return nil
}

func (i *InferLatencyInjector) Recover(ctx context.Context) error {
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

func buildProxyConfig(upstream, latency, jitter, prefix string, dropRate float64, seed int64) (proxy.Config, error) {
	cfg := proxy.Config{
		Upstream:      upstream,
		PathPrefix:    prefix,
		TokenDropRate: dropRate,
		Seed:          seed,
	}
	if latency != "" {
		d, err := time.ParseDuration(latency)
		if err != nil {
			return cfg, fmt.Errorf("latency: %w", err)
		}
		cfg.Latency = d
	}
	if jitter != "" && jitter != "0s" && jitter != "0" {
		d, err := time.ParseDuration(jitter)
		if err != nil {
			return cfg, fmt.Errorf("jitter: %w", err)
		}
		cfg.Jitter = d
	}
	return cfg, nil
}

func defaultIfEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func splitHostPort(addr string) (string, int, error) {
	for idx := len(addr) - 1; idx >= 0; idx-- {
		if addr[idx] == ':' {
			host := addr[:idx]
			portStr := addr[idx+1:]
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return "", 0, fmt.Errorf("port %q is not an integer", portStr)
			}
			if port <= 0 || port > 65535 {
				return "", 0, fmt.Errorf("port %d out of range", port)
			}
			return host, port, nil
		}
	}
	return "", 0, fmt.Errorf("missing :port in %q", addr)
}
