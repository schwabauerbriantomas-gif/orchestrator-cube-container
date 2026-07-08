// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package utils

import (
	"os"
	"testing"
)

func SkipCI(t testing.TB) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping testing in CI environment")
	}
}
