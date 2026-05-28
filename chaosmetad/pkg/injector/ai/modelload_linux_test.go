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
	"os"
	"path/filepath"
	"testing"
)

func TestModelLoadValidator(t *testing.T) {
	i := &ModelLoadInjector{}
	i.SetDefault()
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("empty path should fail")
	}
	i.Args.Path = "relative/path"
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("relative path should fail")
	}
	i.Args.Path = "/definitely/not/a/real/path/xyz123"
	if err := i.Validator(context.Background()); err == nil {
		t.Fatal("nonexistent path should fail")
	}
	tmp, err := os.CreateTemp("", "chaosmeta-modelload-*")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	i.Args.Path = tmp.Name()
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("real file should pass: %v", err)
	}
}

func TestModelLoadInjectRecoverFile(t *testing.T) {
	dir := t.TempDir()
	// Make a fake model weight file.
	weights := filepath.Join(dir, "pytorch_model.bin")
	if err := os.WriteFile(weights, []byte("WEIGHTS"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	i := &ModelLoadInjector{}
	i.SetDefault()
	i.Info.Uid = "modelload-test1"
	i.Args.Path = weights
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if _, err := os.Stat(weights); !os.IsNotExist(err) {
		t.Fatalf("original path should be gone, stat err=%v", err)
	}
	if len(i.Runtime.Renames) != 1 {
		t.Fatalf("runtime renames=%d", len(i.Runtime.Renames))
	}
	hidden := i.Runtime.Renames[weights]
	if _, err := os.Stat(hidden); err != nil {
		t.Fatalf("hidden file missing: %v", err)
	}
	if err := i.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got, err := os.ReadFile(weights); err != nil || string(got) != "WEIGHTS" {
		t.Fatalf("restored=%q err=%v", got, err)
	}
	if _, err := os.Stat(hidden); !os.IsNotExist(err) {
		t.Fatalf("hidden file should be gone after recover")
	}
}

func TestModelLoadInjectRecoverDir(t *testing.T) {
	parent := t.TempDir()
	modelDir := filepath.Join(parent, "Llama-3-8B")
	if err := os.MkdirAll(modelDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, f := range []string{"config.json", "pytorch_model.bin", "tokenizer.json"} {
		if err := os.WriteFile(filepath.Join(modelDir, f), []byte(f), 0644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	i := &ModelLoadInjector{}
	i.SetDefault()
	i.Info.Uid = "ml-dir-1"
	i.Args.Path = modelDir
	if err := i.Validator(context.Background()); err != nil {
		t.Fatalf("Validator: %v", err)
	}
	if err := i.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if _, err := os.Stat(modelDir); !os.IsNotExist(err) {
		t.Fatal("dir should be hidden")
	}
	if err := i.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if info, err := os.Stat(modelDir); err != nil || !info.IsDir() {
		t.Fatalf("restored dir: err=%v info=%+v", err, info)
	}
	entries, _ := os.ReadDir(modelDir)
	if len(entries) != 3 {
		t.Fatalf("restored dir entries=%d", len(entries))
	}
}

func TestModelLoadConflictOnRecover(t *testing.T) {
	dir := t.TempDir()
	weights := filepath.Join(dir, "weights.bin")
	if err := os.WriteFile(weights, []byte("v1"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	i := &ModelLoadInjector{}
	i.SetDefault()
	i.Info.Uid = "ml-conflict"
	i.Args.Path = weights
	_ = i.Validator(context.Background())
	if err := i.Inject(context.Background()); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	// Someone re-created the file (e.g. a CI restart or operator).
	if err := os.WriteFile(weights, []byte("v2"), 0644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := i.Recover(context.Background()); err == nil {
		t.Fatal("Recover should fail loudly when target exists")
	}
	// Hidden file must still be there so an operator can intervene.
	hidden := hiddenName(weights, "ml-conflict")
	if _, err := os.Stat(hidden); err != nil {
		t.Fatalf("hidden file should still exist: %v", err)
	}
}

func TestModelLoadRefusesIfHiddenExists(t *testing.T) {
	dir := t.TempDir()
	weights := filepath.Join(dir, "w.bin")
	if err := os.WriteFile(weights, []byte("w"), 0644); err != nil {
		t.Fatal(err)
	}
	// Pre-create the would-be hidden path.
	if err := os.WriteFile(hiddenName(weights, "u1"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	i := &ModelLoadInjector{}
	i.SetDefault()
	i.Info.Uid = "u1"
	i.Args.Path = weights
	_ = i.Validator(context.Background())
	if err := i.Inject(context.Background()); err == nil {
		t.Fatal("Inject should refuse to overwrite an existing hidden file")
	}
	// Original is untouched.
	if got, _ := os.ReadFile(weights); string(got) != "w" {
		t.Fatalf("original modified: %q", got)
	}
}
