/*
 * Copyright 2022-2026 Chaos Meta Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

// Package proxy is the shared HTTP reverse-proxy used by AI inference
// injectors. It supports two modes of failure:
//
//   - Latency: sleep before forwarding the request (jitter optional) and an
//     additional per-write delay on streaming responses.
//   - TokenDrop: parse Server-Sent Events / NDJSON streams and silently drop
//     a configurable fraction of events on their way back to the client.
//
// The proxy is exported as a plain net/http.Handler so the test suite can
// drive it through httptest without spawning a subprocess.
package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures one proxy. Upstream is the target inference server.
// Latency is applied before the upstream call returns to the client.
// Jitter, when > 0, adds [0, Jitter) uniform extra delay.
// PathPrefix, when non-empty, restricts the injection to requests whose path
// starts with the prefix (e.g. "/v1/" or "/models/").
// TokenDropRate is in [0, 1]; non-zero engages SSE/NDJSON stream parsing.
// Seed makes TokenDrop deterministic in tests; if 0 the proxy seeds from time.
type Config struct {
	Upstream      string
	Latency       time.Duration
	Jitter        time.Duration
	PathPrefix    string
	TokenDropRate float64
	Seed          int64
}

// Validate returns nil if the config is usable.
func (c Config) Validate() error {
	if c.Upstream == "" {
		return fmt.Errorf("upstream is empty")
	}
	if _, err := url.Parse(c.Upstream); err != nil {
		return fmt.Errorf("upstream invalid: %w", err)
	}
	if c.Latency < 0 {
		return fmt.Errorf("latency must be >= 0")
	}
	if c.Jitter < 0 {
		return fmt.Errorf("jitter must be >= 0")
	}
	if c.TokenDropRate < 0 || c.TokenDropRate > 1 {
		return fmt.Errorf("token_drop_rate must be in [0,1]")
	}
	return nil
}

// Stats is a thread-safe counter set exported for assertions.
type Stats struct {
	Requests       atomic.Uint64
	StreamRequests atomic.Uint64
	EventsForward  atomic.Uint64
	EventsDropped  atomic.Uint64
}

// Snapshot returns a non-atomic copy.
func (s *Stats) Snapshot() (req, stream, fwd, dropped uint64) {
	return s.Requests.Load(), s.StreamRequests.Load(),
		s.EventsForward.Load(), s.EventsDropped.Load()
}

// Handler is the http.Handler returned by New.
type Handler struct {
	cfg    Config
	rng    *rand.Rand
	rngMu  sync.Mutex
	rproxy *httputil.ReverseProxy
	Stats  Stats
}

// New constructs a Handler. The returned Handler is safe for concurrent use.
func New(cfg Config) (*Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	target, _ := url.Parse(cfg.Upstream)
	rp := httputil.NewSingleHostReverseProxy(target)
	// Preserve incoming Host so OpenAI-style endpoints behave naturally.
	origDirector := rp.Director
	rp.Director = func(r *http.Request) {
		origDirector(r)
		r.Host = target.Host
	}
	seed := cfg.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	h := &Handler{
		cfg:    cfg,
		rng:    rand.New(rand.NewSource(seed)),
		rproxy: rp,
	}
	rp.ModifyResponse = h.modifyResponse
	return h, nil
}

func (h *Handler) matchesPath(p string) bool {
	if h.cfg.PathPrefix == "" {
		return true
	}
	return strings.HasPrefix(p, h.cfg.PathPrefix)
}

// ServeHTTP applies the configured delay then forwards.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.Stats.Requests.Add(1)
	if h.matchesPath(r.URL.Path) {
		h.sleepForRequest(r.Context())
	}
	h.rproxy.ServeHTTP(w, r)
}

func (h *Handler) sleepForRequest(ctx context.Context) {
	d := h.cfg.Latency
	if h.cfg.Jitter > 0 {
		h.rngMu.Lock()
		d += time.Duration(h.rng.Int63n(int64(h.cfg.Jitter)))
		h.rngMu.Unlock()
	}
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// modifyResponse rewrites streamed bodies to drop a fraction of SSE/NDJSON
// events. For non-streaming responses it is a no-op.
func (h *Handler) modifyResponse(resp *http.Response) error {
	if h.cfg.TokenDropRate <= 0 {
		return nil
	}
	if !h.matchesPath(resp.Request.URL.Path) {
		return nil
	}
	ct := resp.Header.Get("Content-Type")
	if !isStreamingContentType(ct) {
		return nil
	}
	h.Stats.StreamRequests.Add(1)
	pr, pw := io.Pipe()
	orig := resp.Body
	go func() {
		defer orig.Close()
		defer pw.Close()
		if err := h.streamDrop(orig, pw, ct); err != nil && err != io.EOF {
			_ = pw.CloseWithError(err)
		}
	}()
	resp.Body = pr
	// Stream bodies are unknown-length now.
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	return nil
}

func isStreamingContentType(ct string) bool {
	ct = strings.ToLower(ct)
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		return true
	case strings.HasPrefix(ct, "application/x-ndjson"):
		return true
	case strings.HasPrefix(ct, "application/stream+json"):
		return true
	}
	return false
}

// streamDrop reads events one at a time, randomly drops a fraction, and
// writes the survivors out. For SSE an event is delimited by a blank line;
// for NDJSON it is delimited by '\n'.
func (h *Handler) streamDrop(src io.Reader, dst io.Writer, ct string) error {
	br := bufio.NewReader(src)
	sse := strings.HasPrefix(strings.ToLower(ct), "text/event-stream")
	for {
		event, err := readEvent(br, sse)
		if len(event) > 0 {
			if h.shouldDrop() {
				h.Stats.EventsDropped.Add(1)
			} else {
				if _, werr := dst.Write(event); werr != nil {
					return werr
				}
				h.Stats.EventsForward.Add(1)
				if flusher, ok := dst.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
		if err != nil {
			return err
		}
	}
}

func (h *Handler) shouldDrop() bool {
	h.rngMu.Lock()
	defer h.rngMu.Unlock()
	return h.rng.Float64() < h.cfg.TokenDropRate
}

// readEvent reads one logical event from the stream. For SSE, an event is
// the bytes up to (and including) the blank-line terminator. For NDJSON,
// an event is one line including the trailing newline.
func readEvent(br *bufio.Reader, sse bool) ([]byte, error) {
	if !sse {
		line, err := br.ReadBytes('\n')
		return line, err
	}
	var buf []byte
	for {
		line, err := br.ReadBytes('\n')
		buf = append(buf, line...)
		if err != nil {
			return buf, err
		}
		// SSE event terminator: a blank line (\n or \r\n).
		if string(line) == "\n" || string(line) == "\r\n" {
			return buf, nil
		}
	}
}
