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
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/injector"
	"github.com/traas-stack/chaosmeta/chaosmetad/pkg/log"
)

func init() {
	injector.Register(TargetAI, FaultModelLoad, func() injector.IInjector { return &ModelLoadInjector{} })
}

// ModelLoadInjector simulates "model not available / model load failure" by
// renaming the on-disk artifacts out of the way for the duration of the
// experiment. New workers that try to load the model will fail; existing
// workers that have the weights mmapped are unaffected (which mirrors the
// behavior of real model-server crashes during weight reload).
//
// The rename is atomic on the same filesystem. Recover renames everything
// back. If the chaosmetad process dies between Inject and Recover, run
// `chaosmetad recover --uid <uid>` to clean up — the runtime records each
// (original, hidden) pair.
type ModelLoadInjector struct {
	injector.BaseInjector
	Args    ModelLoadArgs
	Runtime ModelLoadRuntime
}

type ModelLoadArgs struct {
	// Path is the file or directory to hide. Required.
	Path string `json:"path"`
}

type ModelLoadRuntime struct {
	// Renames maps original path -> hidden path. Populated by Inject.
	Renames map[string]string `json:"renames,omitempty"`
}

func (i *ModelLoadInjector) GetArgs() interface{}    { return &i.Args }
func (i *ModelLoadInjector) GetRuntime() interface{} { return &i.Runtime }

func (i *ModelLoadInjector) SetOption(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&i.Args.Path, "path", "p", "",
		"absolute path to a model file or directory to hide (required)")
}

func (i *ModelLoadInjector) Validator(ctx context.Context) error {
	if err := i.BaseInjector.Validator(ctx); err != nil {
		return err
	}
	if i.Args.Path == "" {
		return fmt.Errorf("\"path\" must be provided")
	}
	if !filepath.IsAbs(i.Args.Path) {
		return fmt.Errorf("\"path\"[%s] must be absolute", i.Args.Path)
	}
	if _, err := os.Stat(i.Args.Path); err != nil {
		return fmt.Errorf("\"path\"[%s] not accessible: %s", i.Args.Path, err.Error())
	}
	return nil
}

func (i *ModelLoadInjector) Inject(ctx context.Context) error {
	logger := log.GetLogger(ctx)
	hidden := hiddenName(i.Args.Path, i.Info.Uid)
	if _, err := os.Stat(hidden); err == nil {
		return fmt.Errorf("hidden path %q already exists; refusing to overwrite", hidden)
	}
	if err := os.Rename(i.Args.Path, hidden); err != nil {
		return fmt.Errorf("rename %q -> %q: %w", i.Args.Path, hidden, err)
	}
	if i.Runtime.Renames == nil {
		i.Runtime.Renames = make(map[string]string)
	}
	i.Runtime.Renames[i.Args.Path] = hidden
	logger.Infof("model_load: hid %q at %q", i.Args.Path, hidden)
	return nil
}

func (i *ModelLoadInjector) Recover(ctx context.Context) error {
	if i.BaseInjector.Recover(ctx) == nil {
		return nil
	}
	logger := log.GetLogger(ctx)
	var firstErr error
	for original, hidden := range i.Runtime.Renames {
		if _, err := os.Stat(hidden); err != nil {
			logger.Warnf("hidden path %q already gone: %s", hidden, err.Error())
			continue
		}
		if _, err := os.Stat(original); err == nil {
			err := fmt.Errorf("cannot restore %q: target already exists (manual recovery required, hidden copy at %q)", original, hidden)
			if firstErr == nil {
				firstErr = err
			}
			logger.Warn(err.Error())
			continue
		}
		if err := os.Rename(hidden, original); err != nil {
			logger.Warnf("rename %q -> %q: %s", hidden, original, err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		delete(i.Runtime.Renames, original)
	}
	return firstErr
}

func hiddenName(path, uid string) string {
	dir, base := filepath.Split(path)
	return filepath.Join(dir, fmt.Sprintf(".chaosmeta_hidden_%s_%s", uid, base))
}
