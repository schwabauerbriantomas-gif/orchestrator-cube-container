// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package server

import "testing"

func TestIsCriticalCubeletPlugin(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "cubelet workflow plugin", id: "io.cubelet.workflow.v1.workflow", want: true},
		{name: "cubelet service plugin", id: "io.cubelet.cubebox-service.v1.gc-service", want: true},
		{name: "containerd builtin plugin", id: "io.containerd.grpc.v1.healthcheck", want: false},
		{name: "empty id", id: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCriticalCubeletPlugin(tc.id); got != tc.want {
				t.Fatalf("isCriticalCubeletPlugin(%q)=%v, want %v", tc.id, got, tc.want)
			}
		})
	}
}
