// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package utils

import (
	"context"
	"syscall"
	"time"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/log"
)

func ProcessExists(ctx context.Context, pid int) bool {
	if pid <= 0 {
		return false
	}
	ctxTmp, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	for {
		select {
		case <-ctxTmp.Done():
			log.G(ctx).Warnf("process[%d] check timeout,still exist,%v", pid, ctxTmp.Err())
			return true
		default:

			if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
				log.G(ctx).Debugf("process[%d] not exist,%v", pid, err)
				return false
			}

			time.Sleep(10 * time.Millisecond)
		}
	}
}
