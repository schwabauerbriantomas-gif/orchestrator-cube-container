// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package utils

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestProcessExists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	flag := ProcessExists(ctx, 1)
	assert.True(t, flag)

	flag = ProcessExists(ctx, 1000000000)
	assert.False(t, flag)
}
