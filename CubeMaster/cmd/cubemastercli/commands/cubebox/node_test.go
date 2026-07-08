// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package cubebox

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/base/node"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = oldStdout
	})

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(data)
}

func TestPrintNodeSummaryOmitsCapacityColumns(t *testing.T) {
	output := captureStdout(t, func() {
		printNodeSummary([]*node.Node{
			{
				InsID:         "node-1",
				IP:            "10.0.0.1",
				InstanceType:  "S5",
				Zone:          "ap-shanghai-1",
				CPUType:       "AMD",
				Healthy:       true,
				HostStatus:    "RUNNING",
				QuotaCpu:      8000,
				QuotaCpuUsage: 2000,
				QuotaMem:      16384,
				QuotaMemUsage: 4096,
				MvmNum:        12,
				Score:         0.875,
			},
		}, false)
	})

	for _, unwanted := range []string{"CPU_FREE", "MEM_FREE_MIB", "MVM_NUM", "SCORE", "6000", "12288", "12", "0.8750"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("output=%q, should not contain %q", output, unwanted)
		}
	}
	for _, wanted := range []string{"NODE_ID", "NODE_IP", "INSTANCE_TYPE", "HOST_STATUS", "node-1", "10.0.0.1", "RUNNING"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("output=%q, missing %q", output, wanted)
		}
	}
}

func TestPrintNodeSummaryScoreOnlyKeepsScoreColumns(t *testing.T) {
	output := captureStdout(t, func() {
		printNodeSummary([]*node.Node{
			{
				InsID: "node-1",
				Score: 0.875,
			},
		}, true)
	})

	for _, wanted := range []string{"NODE_ID", "SCORE", "METRIC_UPDATE", "0.8750"} {
		if !strings.Contains(output, wanted) {
			t.Fatalf("output=%q, missing %q", output, wanted)
		}
	}
}
