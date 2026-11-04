// LESSON 18 — CONFIG & LOGGING (part 3: building the logger)
//
// log/slog (Go 1.21+) is Go's built-in STRUCTURED logger. Instead of
// gluing values into a sentence, you log a fixed message plus key/value
// pairs:
//
//	PHP (Monolog / Laravel)                     Go (slog)
//	Log::info('Order placed', ['id' => 42]);    logger.Info("order placed", "id", 42)
//	Log::withContext(['job' => 7]);             jobLog := logger.With("job", 7)
//	LOG_LEVEL=debug in .env                     slog.HandlerOptions{Level: ...}
//	a Monolog handler + formatter               a slog.Handler (Text or JSON)
//
// The pieces:
//   - *slog.Logger is what you call: Info, Warn, With...
//   - a slog.Handler decides what happens to each record: filter by level,
//     format as text or JSON, write it somewhere. Swap the handler and
//     every log call in the app changes, with no other code touched.
package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// NewLogger builds the app's logger. level is a slog.Leveler, so it can be
// a fixed slog.Level or a *slog.LevelVar that changes while the program
// runs (section 8 of main.go).
//
// hideTime drops the timestamp. This lesson uses it so the output is the
// same every run. Real apps keep the time!
func NewLogger(w io.Writer, format string, level slog.Leveler, hideTime bool) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
		// ReplaceAttr is called for every key/value before it's written.
		// It's where you rename, reformat or remove attributes.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if hideTime && a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.Attr{} // an empty Attr means "leave it out"
			}
			if isSensitive(a.Key) {
				return slog.String(a.Key, redacted)
			}
			return a
		},
	}

	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(contextHandler{h})
}

// isSensitive is a safety net for secrets logged as plain strings, e.g.
// logger.Info("login", "password", pw). The Secret type in config.go is the
// first line of defence; this catches the cases where someone forgot it.
// It's the same idea as Laravel's $dontFlash list for passwords.
func isSensitive(key string) bool {
	key = strings.ToLower(key)
	for _, word := range []string{"password", "secret", "token", "authorization", "api_key"} {
		if strings.Contains(key, word) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------
// Request IDs from context
// ---------------------------------------------------------------------

// ctxKey is an unexported type for context keys (lesson 14). No other
// package can make a key equal to it, so values can't clash.
type ctxKey struct{}

// WithRequestID stores a request ID in ctx, usually in HTTP middleware.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// contextHandler wraps another handler and adds request_id to every
// record logged with a ...Context method (InfoContext, ErrorContext...).
// Code deep in the call stack then gets the ID in its logs just by passing
// ctx along, which it does anyway.
//
// It embeds slog.Handler (lesson 05), so Enabled comes from the wrapped
// handler for free. We only replace the methods we need to change.
type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := ctx.Value(ctxKey{}).(string); ok {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs and WithGroup MUST be overridden too. The embedded versions
// would return the INNER handler, unwrapped, so any logger made with
// logger.With(...) would silently lose the request IDs. (There's a test
// for this in logging_test.go.)
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
