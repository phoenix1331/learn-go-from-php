package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg, err := Load(nil, mapEnv(nil)) // an empty environment
	if err != nil {
		t.Fatal(err)
	}
	// Config holds a slice, so == won't compile. reflect.DeepEqual compares
	// field by field, like PHP's == on arrays.
	if !reflect.DeepEqual(cfg, defaults()) {
		t.Errorf("got %+v, want defaults %+v", cfg, defaults())
	}
}

func TestLoadFromEnv(t *testing.T) {
	cfg, err := Load(nil, mapEnv(map[string]string{
		"APP_ENV":      "production",
		"PORT":         "9000",
		"LOG_LEVEL":    "debug",
		"LOG_FORMAT":   "json",
		"HTTP_TIMEOUT": "1m30s",
		"WORKERS":      " 16 ", // spaces are trimmed
		"CORS_ORIGINS": "https://a.test, https://b.test,,",
		"DB_PASSWORD":  "hunter2",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		AppEnv:     "production",
		Port:       9000,
		LogLevel:   slog.LevelDebug,
		LogFormat:  "json",
		Timeout:    90 * time.Second,
		Workers:    16,
		Origins:    []string{"https://a.test", "https://b.test"},
		DBPassword: "hunter2",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("\ngot  %+v\nwant %+v", cfg, want)
	}
	// %+v redacts the password in BOTH lines above, so check it directly.
	if cfg.DBPassword.Reveal() != "hunter2" {
		t.Error("DB_PASSWORD was not loaded")
	}
}

func TestBlankMeansDefault(t *testing.T) {
	cfg, err := Load(nil, mapEnv(map[string]string{"PORT": "", "WORKERS": "   "}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.Workers != 4 {
		t.Errorf("got port %d workers %d, want the defaults", cfg.Port, cfg.Workers)
	}
}

func TestPrecedence(t *testing.T) {
	env := mapEnv(map[string]string{"PORT": "9000", "LOG_LEVEL": "warn"})

	// A table-driven test (lesson 12).
	tests := []struct {
		name      string
		args      []string
		wantPort  int
		wantLevel slog.Level
	}{
		{"env only", nil, 9000, slog.LevelWarn},
		{"flag beats env", []string{"-port=7000"}, 7000, slog.LevelWarn},
		{"both flags", []string{"-port", "7000", "-log-level", "error"}, 7000, slog.LevelError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.args, env)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Port != tt.wantPort || cfg.LogLevel != tt.wantLevel {
				t.Errorf("got port %d level %v, want %d %v", cfg.Port, cfg.LogLevel, tt.wantPort, tt.wantLevel)
			}
		})
	}
}

func TestReportsEveryProblem(t *testing.T) {
	_, err := Load(nil, mapEnv(map[string]string{
		"APP_ENV":      "prod", // not one of the allowed names
		"PORT":         "70000",
		"LOG_LEVEL":    "loud",
		"LOG_FORMAT":   "xml",
		"HTTP_TIMEOUT": "5",
		"WORKERS":      "many",
	}))
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	for _, key := range []string{"APP_ENV", "PORT", "LOG_LEVEL", "LOG_FORMAT", "HTTP_TIMEOUT", "WORKERS"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error doesn't mention %s:\n%v", key, err)
		}
	}
	// WORKERS=many fails strconv.Atoi, and that error is still reachable.
	if !errors.Is(err, strconv.ErrSyntax) {
		t.Error("errors.Is(err, strconv.ErrSyntax) = false, want true")
	}
}

func TestProductionNeedsPassword(t *testing.T) {
	_, err := Load(nil, mapEnv(map[string]string{"APP_ENV": "production"}))
	if err == nil || !strings.Contains(err.Error(), "DB_PASSWORD") {
		t.Errorf("got %v, want a DB_PASSWORD error", err)
	}
	// The same rule, triggered by a flag instead of the environment.
	_, err = Load([]string{"-env=production"}, mapEnv(nil))
	if err == nil {
		t.Error("-env=production without DB_PASSWORD: want an error, got nil")
	}
}

func TestUnknownFlag(t *testing.T) {
	_, err := Load([]string{"-nope"}, mapEnv(nil))
	if err == nil {
		t.Error("want an error for an unknown flag, got nil")
	}
}

// t.Setenv sets a REAL environment variable and puts it back after the
// test. Handy when you can't inject a lookup function. A test using it
// can't call t.Parallel(), because the environment is shared by the whole
// process.
func TestRealEnvironment(t *testing.T) {
	t.Setenv("PORT", "1234")
	cfg, err := Load(nil, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 1234 {
		t.Errorf("got port %d, want 1234", cfg.Port)
	}
}

// Every way a value can be printed must hide the secret.
func TestSecretNeverPrinted(t *testing.T) {
	cfg := defaults()
	cfg.DBPassword = "hunter2"

	var logs strings.Builder
	NewLogger(&logs, "text", slog.LevelInfo, false).Info("cfg", "config", cfg, "pw", cfg.DBPassword)
	NewLogger(&logs, "json", slog.LevelInfo, false).Info("cfg", "config", cfg, "pw", cfg.DBPassword)
	jsonBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	outputs := map[string]string{
		"%v":           fmt.Sprintf("%v", cfg),
		"%+v":          fmt.Sprintf("%+v", cfg),
		"%#v":          fmt.Sprintf("%#v", cfg),
		"%s on Secret": fmt.Sprintf("%s", cfg.DBPassword),
		"json.Marshal": string(jsonBytes),
		"slog":         logs.String(),
	}
	for name, out := range outputs {
		if strings.Contains(out, "hunter2") {
			t.Errorf("%s leaked the password: %s", name, out)
		}
	}
}
