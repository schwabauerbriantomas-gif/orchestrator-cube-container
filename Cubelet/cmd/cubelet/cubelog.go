// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package main

import (
	"fmt"

	"github.com/urfave/cli/v2"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/log"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/version"
	cubelog "github.com/tencentcloud/CubeSandbox/cubelog"
)

func initCubeLog(context *cli.Context, module, logPath string) {

	cubelog.SetModuleName(module)

	cubelog.EnableFileLog()
	cubelog.SetSkipCallerDepth(0)

	cubelog.SetLevel(cubelog.INFO)

	cubelog.SetVersion(version.ShowVersion())

	cubelog.EnableLogMetric()

	if logPath == "" {
		logPath = fmt.Sprintf("/data/log/%s", module)
	}
	cubelog.Create(logPath)
	traceLogName := fmt.Sprintf("%s-stat", module)

	num := context.Int("log-roll-num")
	size := context.Int("log-roll-size")

	cubelog.SetTraceOutput(cubelog.NewRollFileWriter(logPath, traceLogName, num, size))

	reqLogName := fmt.Sprintf("%s-req", module)

	reqLogWriter := cubelog.NewRollFileWriter(logPath, reqLogName, num, size)
	cubelog.SetOutput(reqLogWriter)

	auditLogger := cubelog.GetLogger("audit")
	auditLogger.SetOutput(cubelog.NewRollFileWriter(logPath, "Audit", num, size))
	log.AuditLogger = auditLogger

	debugStdoutReqLogName := fmt.Sprintf("%s-req", "Stdout")
	debugStdoutLogger := cubelog.GetLogger("stdout")
	debugStdoutLogger.SetOutput(cubelog.NewRollFileWriter(logPath, debugStdoutReqLogName, num, size))

	log.SetContainerdLog()

	log.SetKlog()
}
