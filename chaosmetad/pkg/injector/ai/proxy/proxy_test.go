/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	good := Config{Upstream: "http://127.0.0.1:8080", Latency: 100 * time.Millisecond}
	if err := good.Validate(); err != nil {
		t.Fatalf("good config should validate: %v", err)
	}
	bad := []Config{
		{Upstream: ""},
		{Upstream: "http://x", Latency: -1},
		{Upstream: "http://x", Jitter: -1},
		{Upstream: "http://x", TokenDropRate: 1.5},
		{Upstream: "http://x", TokenDropRate: -0.1},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d: bad config should not validate", i)
		}
	}
}

func TestLatencyInjection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	h, err := New(Config{Upstream: upstream.URL, Latency: 80 * time.Millisecond})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	start := time.Now()
	resp, err := http.Get(front.URL + "/v1/completions")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	elapsed := time.Since(start)
	if elapsed < 75*time.Millisecond {
		t.Fatalf("expected ~80ms latency, got %v", elapsed)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestLatencyPathPrefixFilter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	h, err := New(Config{Upstream: upstream.URL, Latency: 80 * time.Millisecond, PathPrefix: "/v1/"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	// Outside prefix: no latency.
	start := time.Now()
	resp, _ := http.Get(front.URL + "/healthz")
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("non-prefix request should not be delayed, got %v", d)
	}
	// Inside prefix: latency applied.
	start = time.Now()
	resp, _ = http.Get(front.URL + "/v1/models")
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if d := time.Since(start); d < 70*time.Millisecond {
		t.Fatalf("prefix request should be delayed, got %v", d)
	}
}

func TestSSETokenDrop(t *testing.T) {
	// Upstream emits 10 SSE events.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 10; i++ {
			fmt.Fprintf(w, "data: token-%d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	// Seed=1 makes the drop pattern deterministic for this PRNG.
	h, err := New(Config{Upstream: upstream.URL, TokenDropRate: 0.5, Seed: 1})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := http.Get(front.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	events := strings.Count(string(body), "data: token-")
	if events == 10 {
		t.Fatalf("token_drop did nothing; got all 10 events")
	}
	if events == 0 {
		t.Fatalf("token_drop dropped everything; got 0 events")
	}
	req, _, fwd, dropped := h.Stats.Snapshot()
	if req == 0 {
		t.Fatalf("req counter not incremented")
	}
	if fwd+dropped == 0 {
		t.Fatalf("no events accounted for; fwd=%d dropped=%d", fwd, dropped)
	}
}

func TestSSEStreamPreservedOnZeroRate(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: x-%d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	h, _ := New(Config{Upstream: upstream.URL}) // no drop, no latency
	front := httptest.NewServer(h)
	defer front.Close()
	resp, _ := http.Get(front.URL + "/v1/sse")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := strings.Count(string(body), "data: x-"); got != 3 {
		t.Fatalf("zero-rate should pass through; got %d events: %q", got, string(body))
	}
}

func TestNDJSONTokenDrop(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 20; i++ {
			fmt.Fprintf(w, "{\"i\":%d}\n", i)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	h, _ := New(Config{Upstream: upstream.URL, TokenDropRate: 0.8, Seed: 42})
	front := httptest.NewServer(h)
	defer front.Close()
	resp, _ := http.Get(front.URL + "/v1/x")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	lines := 0
	for _, l := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if l != "" {
			lines++
		}
	}
	if lines == 20 {
		t.Fatalf("expected drops at rate=0.8, got all 20 lines")
	}
	if lines == 0 {
		t.Fatalf("expected at least one survivor")
	}
}

func TestNonStreamingResponseUntouched(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"text":"hello"}]}`))
	}))
	defer upstream.Close()
	h, _ := New(Config{Upstream: upstream.URL, TokenDropRate: 0.99, Seed: 1})
	front := httptest.NewServer(h)
	defer front.Close()
	resp, _ := http.Get(front.URL + "/v1/completions")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "hello") {
		t.Fatalf("non-stream body should pass through: %q", body)
	}
}

func TestTokenDropDeterministic(t *testing.T) {
	// Same seed must produce the same drop pattern across runs.
	makeUpstream := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			for i := 0; i < 50; i++ {
				fmt.Fprintf(w, "data: %d\n\n", i)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}))
	}
	run := func(seed int64) string {
		up := makeUpstream()
		defer up.Close()
		h, _ := New(Config{Upstream: up.URL, TokenDropRate: 0.5, Seed: seed})
		front := httptest.NewServer(h)
		defer front.Close()
		resp, _ := http.Get(front.URL + "/v1/x")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(body)
	}
	a := run(99)
	b := run(99)
	if a != b {
		t.Fatalf("same seed produced different output:\n%q\nvs\n%q", a, b)
	}
}
