// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

// Package log provides unified log wrapping and context management.
//
// Core features:
// 1. CubeWrapperLogEntry - a wrapper around CubeLog.Entry providing convenient logging methods
// 2. Context integration - supports passing and retrieving logger instances via context
// 3. Log bridging - unifies klog, logrus, and containerd log into CubeLog
//
// Usage example:
//
//	// Get the logger for the current context
//	logger := log.GetLogger(ctx)
//	logger.WithField("request_id", "123").Info("message")
//
//	// Create a new context and associate a logger with it
//	newCtx := log.WithLogger(ctx, logger)
//	// All logging in the new context will use this logger
//
// Logging pipeline:
//
//	Business code (CubeLog.Entry)
//	    ↓
//	CubeWrapperLogEntry (this package)
//	    ↓
//	context passing and association
//	    ↓
//	klog/logrus/containerd log bridging
//	    ↓
//	log file output
package log

import (
	"context"

	"github.com/tencentcloud/CubeSandbox/cubelog"
)

var (
	G = GetLogger

	L = GetLogger(context.Background())
)

type loggerKey struct{}

type CubeWrapperLogEntry struct {
	*CubeLog.Entry
}

func NewWrapperLogEntry(entry *CubeLog.Entry) *CubeWrapperLogEntry {
	return &CubeWrapperLogEntry{Entry: entry}
}

func WithLogger(ctx context.Context, e *CubeWrapperLogEntry) context.Context {
	return context.WithValue(ctx, loggerKey{}, e)
}

func (w *CubeWrapperLogEntry) WithError(err error) *CubeWrapperLogEntry {
	if err == nil {
		return w
	}
	return w.WithFields(CubeLog.Fields{"error": err.Error()})
}

func (w *CubeWrapperLogEntry) WithField(key string, value any) *CubeWrapperLogEntry {
	return w.WithFields(CubeLog.Fields{key: value})
}

func (w *CubeWrapperLogEntry) WithFields(fields CubeLog.Fields) *CubeWrapperLogEntry {
	return NewWrapperLogEntry(w.Entry.WithFields(fields))
}

func (w *CubeWrapperLogEntry) Tracef(format string, v ...interface{}) {

}

func (w *CubeWrapperLogEntry) Info(args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		args = append(args, extraContent)
	}
	w.Entry.Info(args...)
}

func (w *CubeWrapperLogEntry) Infof(format string, args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		format = format + "%s"
		args = append(args, extraContent)
	}
	w.Entry.Infof(format, args...)
}

func (w *CubeWrapperLogEntry) Debug(args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		args = append(args, extraContent)
	}
	w.Entry.Debug(args...)
}

func (w *CubeWrapperLogEntry) Debugf(format string, args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		format = format + "%s"
		args = append(args, extraContent)
	}
	w.Entry.Debugf(format, args...)
}

func (w *CubeWrapperLogEntry) Warn(args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		args = append(args, extraContent)
	}
	w.Entry.Warn(args...)
}

func (w *CubeWrapperLogEntry) Warnf(format string, args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		format = format + "%s"
		args = append(args, extraContent)
	}
	w.Entry.Warnf(format, args...)
}

func (w *CubeWrapperLogEntry) Error(args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		args = append(args, extraContent)
	}
	w.Entry.Error(args...)
}

func (w *CubeWrapperLogEntry) Errorf(format string, args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		format = format + "%s"
		args = append(args, extraContent)
	}
	w.Entry.Errorf(format, args...)
}

func (w *CubeWrapperLogEntry) Fatal(args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		args = append(args, extraContent)
	}
	w.Entry.Fatal(args...)
}

func (w *CubeWrapperLogEntry) Fatalf(format string, args ...interface{}) {
	extraContent := w.extralFieldsToContent()
	if extraContent != "" {
		format = format + "%s"
		args = append(args, extraContent)
	}
	w.Entry.Fatalf(format, args...)
}

func GetLogger(ctx context.Context) *CubeWrapperLogEntry {
	logger := ctx.Value(loggerKey{})

	if logger == nil {

		return &CubeWrapperLogEntry{Entry: CubeLog.WithContext(ctx)}
	}
	if old, ok := logger.(*CubeWrapperLogEntry); ok {
		return old
	}
	return &CubeWrapperLogEntry{Entry: logger.(*CubeLog.Entry)}
}

func ReNewLogger(ctx context.Context) context.Context {
	old := ctx.Value(loggerKey{})
	if old == nil {
		return ctx
	}
	return WithLogger(ctx, NewWrapperLogEntry(CubeLog.WithContext(ctx)))
}
