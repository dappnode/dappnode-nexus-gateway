package logger

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// ZapLogger wraps zap.SugaredLogger to implement the Logger port.
type ZapLogger struct {
	sugar   *zap.SugaredLogger
	verbose bool
}

func NewZapLogger(level string) (*ZapLogger, error) {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevel()
	verbose := false

	switch level {
	case "debug":
		cfg.Level.SetLevel(zap.DebugLevel)
		verbose = true
	case "info":
		cfg.Level.SetLevel(zap.InfoLevel)
	case "warn":
		cfg.Level.SetLevel(zap.WarnLevel)
	case "error":
		cfg.Level.SetLevel(zap.ErrorLevel)
	default:
		cfg.Level.SetLevel(zap.InfoLevel)
	}

	l, err := cfg.Build(zap.AddCallerSkip(1))
	if err != nil {
		return nil, err
	}

	return &ZapLogger{
		sugar:   l.Sugar(),
		verbose: verbose,
	}, nil
}

func (l *ZapLogger) Debug(msg string, fields ...any) {
	l.sugar.Debugw(msg, fields...)
}

func (l *ZapLogger) Info(msg string, fields ...any) {
	l.sugar.Infow(msg, fields...)
}

func (l *ZapLogger) Warn(msg string, fields ...any) {
	l.sugar.Warnw(msg, fields...)
}

func (l *ZapLogger) Error(msg string, fields ...any) {
	l.sugar.Errorw(msg, fields...)
}

func (l *ZapLogger) Printf(format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}
	l.sugar.Info(msg)
}

func (l *ZapLogger) Verbose() bool {
	return l.verbose
}

func (l *ZapLogger) Sync() {
	l.sugar.Sync()
}
