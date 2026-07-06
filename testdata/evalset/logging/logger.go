// Package logging sets up the application's structured logger.
package logging

import (
	"io"
	"log/slog"
	"os"
)

// Options controls how the logger is constructed.
type Options struct {
	// Level is the minimum level that will be emitted: "debug", "info",
	// "warn", or "error". Defaults to "info" if empty or unrecognized.
	Level string
	// JSON selects JSON output instead of human-readable text, typically
	// enabled in production so log aggregators can parse it.
	JSON bool
	// Output defaults to os.Stderr if nil.
	Output io.Writer
}

// New builds a slog.Logger configured per opts, with "service" and
// "version" attached to every log line so entries can be attributed when
// multiple services share a log stream.
func New(opts Options, service, version string) *slog.Logger {
	output := opts.Output
	if output == nil {
		output = os.Stderr
	}

	handlerOpts := &slog.HandlerOptions{Level: parseLevel(opts.Level)}

	var handler slog.Handler
	if opts.JSON {
		handler = slog.NewJSONHandler(output, handlerOpts)
	} else {
		handler = slog.NewTextHandler(output, handlerOpts)
	}

	return slog.New(handler).With(
		"service", service,
		"version", version,
	)
}

// parseLevel maps a level name to a slog.Level, defaulting to Info for an
// empty or unrecognized name rather than erroring — logging setup should
// never be the reason the application fails to start.
func parseLevel(name string) slog.Level {
	switch name {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
