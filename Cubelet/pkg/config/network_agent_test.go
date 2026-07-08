// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package config

import (
	"strings"
	"testing"
)

func TestSetNetworkAgentOverrideAppliesDuringPreHandle(t *testing.T) {
	networkAgentOverride = struct {
		enable   bool
		endpoint string
		set      bool
	}{}
	defer func() {
		networkAgentOverride = struct {
			enable   bool
			endpoint string
			set      bool
		}{}
	}()

	SetNetworkAgentOverride(true, "grpc+unix:///tmp/test-network-agent.sock")
	cfg, err := preHandle(&Config{})
	if err != nil {
		t.Fatalf("preHandle failed: %v", err)
	}
	if cfg.Common == nil {
		t.Fatalf("common config is nil")
	}
	if !cfg.Common.EnableNetworkAgent {
		t.Fatalf("expected enable_network_agent to be true")
	}
	if cfg.Common.NetworkAgentEndpoint != "grpc+unix:///tmp/test-network-agent.sock" {
		t.Fatalf("unexpected network agent endpoint: %q", cfg.Common.NetworkAgentEndpoint)
	}
}

func TestPreHandleNormalizesDefaultDNSServers(t *testing.T) {
	cfg, err := preHandle(&Config{
		Common: &CommonConf{
			DefaultDNSServers: []string{" 119.29.29.29 ", "", "1.1.1.1"},
		},
	})
	if err != nil {
		t.Fatalf("preHandle failed: %v", err)
	}

	got := strings.Join(cfg.Common.DefaultDNSServers, ",")
	if got != "119.29.29.29,1.1.1.1" {
		t.Fatalf("unexpected normalized dns servers: %q", got)
	}
}

func TestValidateRejectsInvalidDefaultDNSServers(t *testing.T) {
	err := validate(&Config{
		Common: &CommonConf{
			DefaultDNSServers: []string{"invalid-ip"},
		},
		HostConf: defaultHostConf(),
	})
	if err == nil {
		t.Fatal("expected validate to reject invalid default dns server")
	}
}
