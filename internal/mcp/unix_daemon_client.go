// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/graphmodel"
)

// Call deadlines for the verified single-connection daemon clients (`engine` CLI
// channel). Unlike CallUnixDaemonRecall these clients never spawn a daemon: the caller
// consumes a daemon that is already running, and a missing or unverifiable socket fails
// closed so display surfaces degrade instead of trusting an unverified local socket.
const (
	// unixDaemonHydrateCallTimeout bounds one hydration batch on the verified connection.
	unixDaemonHydrateCallTimeout = 10 * time.Second
	// unixDaemonDocumentsCallTimeout bounds one local-document read batch on the
	// verified connection.
	unixDaemonDocumentsCallTimeout = 10 * time.Second
	// unixDaemonGraphCallTimeout bounds one graph view. The first request after a
	// corpus change may build the engine projection; later requests are cached.
	unixDaemonGraphCallTimeout = 60 * time.Second
	// unixDaemonControlCallTimeout bounds one control notification (cache invalidation),
	// matching the deadline requestUnixDaemonControl uses for its control calls.
	unixDaemonControlCallTimeout = 500 * time.Millisecond
)

// postVerifiedUnixDaemon writes one JSON request over a verified peer connection and
// decodes the single JSON response. Verification (peer credentials plus the health
// identity handshake) happens before the request is written, so an unverified socket
// never receives a business request. Non-200 responses surface the daemon's error
// message; a malformed or over-long stream fails closed.
func postVerifiedUnixDaemon(ctx context.Context, sockPath string, cfg *config.Config, path string, payload any, out any, timeout time.Duration) error {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := unixDaemonConfigID(cfg); err != nil {
		return err
	}
	conn, reader, err := verifiedUnixDaemonConnContext(ctx, sockPath, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(conn); err != nil {
		return fmt.Errorf("daemon %s outcome unknown: %w", path, err)
	}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return fmt.Errorf("daemon %s outcome unknown: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxHTTPBody || reader.Buffered() != 0 {
		return errors.New("invalid daemon response stream")
	}
	if resp.StatusCode != http.StatusOK {
		return unixDaemonStatusError(path, resp.StatusCode, body)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode daemon response (status %d): %w", resp.StatusCode, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("invalid trailing daemon response")
	}
	return nil
}

// unixDaemonStatusError renders one non-200 daemon answer. JSON envelopes carry
// an "error" field; handlers that answer with http.Error write the only
// diagnostic as plain text, so that body is included (bounded) instead of being
// dropped down to a bare status code.
func unixDaemonStatusError(path string, status int, body []byte) error {
	var envelope struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && strings.TrimSpace(envelope.Error) != "" {
		return fmt.Errorf("daemon %s failed with status %d: %s", path, status, envelope.Error)
	}
	if detail := strings.TrimSpace(string(body)); detail != "" {
		if runes := []rune(detail); len(runes) > 256 {
			detail = string(runes[:256]) + "…"
		}
		return fmt.Errorf("daemon %s failed with status %d: %s", path, status, detail)
	}
	return fmt.Errorf("daemon %s failed with status %d", path, status)
}

// CallUnixDaemonHydrate resolves one display-hydration batch over the verified local
// daemon connection (same `/v1/hydrate` handler the console's warm display path uses).
// A running daemon is required; no daemon is ever spawned for a display read.
func CallUnixDaemonHydrate(ctx context.Context, sockPath string, cfg *config.Config, payload UnixHydratePayload) (*UnixHydrateResponse, error) {
	var out UnixHydrateResponse
	if err := postVerifiedUnixDaemon(ctx, sockPath, cfg, "/v1/hydrate", payload, &out, unixDaemonHydrateCallTimeout); err != nil {
		return nil, err
	}
	if !out.OK {
		if strings.TrimSpace(out.Error) != "" {
			return &out, errors.New(out.Error)
		}
		return &out, errors.New("daemon hydrate reported not ok")
	}
	return &out, nil
}

// CallUnixDaemonDocuments resolves one local-document read batch over the verified
// local daemon connection (same `/v1/documents` handler the console's chunk display
// path uses). A running daemon is required; no daemon is ever spawned for a read.
func CallUnixDaemonDocuments(ctx context.Context, sockPath string, cfg *config.Config, payload UnixDocumentPayload) (*UnixDocumentResponse, error) {
	var out UnixDocumentResponse
	if err := postVerifiedUnixDaemon(ctx, sockPath, cfg, "/v1/documents", payload, &out, unixDaemonDocumentsCallTimeout); err != nil {
		return nil, err
	}
	if !out.OK {
		if strings.TrimSpace(out.Error) != "" {
			return &out, errors.New(out.Error)
		}
		return &out, errors.New("daemon documents reported not ok")
	}
	return &out, nil
}

// CallUnixDaemonGraph requests one engine-derived memory graph view over the
// verified local daemon connection. The payload is the same contract the admin
// console relays through `engine graph`; no cloud endpoint is ever consulted.
func CallUnixDaemonGraph(ctx context.Context, sockPath string, cfg *config.Config, payload graphmodel.ViewRequest) (*graphmodel.Response, error) {
	var out graphmodel.Response
	if err := postVerifiedUnixDaemon(ctx, sockPath, cfg, "/v1/graph", payload, &out, unixDaemonGraphCallTimeout); err != nil {
		return nil, err
	}
	return &out, nil
}

// InvalidateUnixDaemonCachesVerified notifies a running daemon at sockPath to drop its
// cached corpus, Noetic, and slot snapshots over one verified connection. notified is
// false with a nil error when no socket exists (nothing to invalidate); a socket that
// fails verification or the control reply is reported as an error.
func InvalidateUnixDaemonCachesVerified(ctx context.Context, sockPath string, cfg *config.Config) (notified bool, err error) {
	if sockPath == "" {
		sockPath = DefaultUnixSocketPath()
	} else {
		sockPath = normalizeUnixSocketPath(sockPath)
	}
	if _, statErr := os.Lstat(sockPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return false, nil
		}
		return false, statErr
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := postVerifiedUnixDaemon(ctx, sockPath, cfg, "/v1/invalidate", map[string]any{}, &out, unixDaemonControlCallTimeout); err != nil {
		return false, err
	}
	if !out.OK {
		return false, errors.New("invalid daemon control response")
	}
	return true, nil
}
