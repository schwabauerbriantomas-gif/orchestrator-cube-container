// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	CubeLog "github.com/tencentcloud/CubeSandbox/cubelog"
)

func TestInitLoggerWithDirCreatesLogFile(t *testing.T) {
	logDir := t.TempDir()
	initErr := initLoggerWithDir(logDir, "info", 2, 1)
	if initErr != nil {
		t.Fatalf("initLoggerWithDir failed: %v", initErr)
	}

	CubeLog.Infof("network-agent test log line")

	logPath := filepath.Join(logDir, defaultModuleName+"-req.log")
	var data []byte
	var err error
	for i := 0; i < 10; i++ {
		data, err = os.ReadFile(logPath)
		if err == nil && len(data) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("ReadFile(%s) failed: %v", logPath, err)
	}
	if len(data) == 0 {
		t.Fatal("expected log file to contain data")
	}
}
