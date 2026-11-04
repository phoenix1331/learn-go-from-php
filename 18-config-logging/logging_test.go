package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// Logs are output too, so they can be tested. Write them to a buffer as
// JSON and decode each line, instead of matching fragile strings.
func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestJSONFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "json", slog.LevelInfo, false)
	logger.Info("order placed", "order_id", 1042)

	lines := decodeLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	got := lines[0]
	// JSON numbers decode into float64 when the target is `any`.
	if got["msg"] != "order placed" || got["level"] != "INFO" || got["order_id"] != 1042.0 {
		t.Errorf("unexpected fields: %v", got)
	}
	if _, ok := got["time"]; !ok {
		t.Error("time is missing; only hideTime=true should remove it")
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	var level slog.LevelVar // the zero value is Info
	logger := NewLogger(&buf, "json", &level, true)

	logger.Debug("hidden")
	level.Set(slog.LevelDebug)
	logger.Debug("shown")
	level.Set(slog.LevelError)
	logger.Warn("hidden too")

	lines := decodeLines(t, &buf)
	if len(lines) != 1 || lines[0]["msg"] != "shown" {
		t.Errorf("got %v, want only the \"shown\" line", lines)
	}
}

func TestRequestIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "json", slog.LevelInfo, true)

	ctx := WithRequestID(t.Context(), "req-1")
	logger.InfoContext(ctx, "with ctx")
	logger.Info("without ctx")

	lines := decodeLines(t, &buf)
	if lines[0]["request_id"] != "req-1" {
		t.Errorf("InfoContext: request_id = %v, want req-1", lines[0]["request_id"])
	}
	if _, ok := lines[1]["request_id"]; ok {
		t.Error("Info without ctx should have no request_id")
	}
}

// The test for the gotcha described on contextHandler.WithAttrs.
func TestRequestIDSurvivesWith(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "json", slog.LevelInfo, true)

	child := logger.With("job_id", 7).WithGroup("job")
	child.InfoContext(WithRequestID(t.Context(), "req-2"), "working", "step", 1)

	got := decodeLines(t, &buf)[0]
	// WithGroup nests everything logged AFTER it, request_id included.
	job, _ := got["job"].(map[string]any)
	if got["job_id"] != 7.0 || job["step"] != 1.0 || job["request_id"] != "req-2" {
		t.Errorf("unexpected fields: %v", got)
	}
}

func TestRedactsSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "text", slog.LevelInfo, true)
	logger.Info("login",
		"user", "ada",
		"password", "hunter2",
		"API_KEY", "sk-123",
		slog.Group("headers", slog.String("Authorization", "Bearer abc")))

	out := buf.String()
	for _, secret := range []string{"hunter2", "sk-123", "Bearer abc"} {
		if strings.Contains(out, secret) {
			t.Errorf("leaked %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "user=ada") {
		t.Errorf("non-secret field missing: %s", out)
	}
}
