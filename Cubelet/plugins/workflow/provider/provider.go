// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package provider

import (
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/tencentcloud/CubeSandbox/Cubelet/network/proto"
)

type NetworkProvider interface {
	ID() string
	SandboxIP() string
	GatewayIP() string
	MacAddress() string
	AllocatedPorts() []proto.PortMapping
	OCISpecOpts() oci.SpecOpts
	GetPersistMetadata() []byte
	FromPersistMetadata([]byte)
	GetNumaNode() int32
	GetNICQueues() int64
	GetPCIMode() string
	IsRetainIP() bool
	GetNetMask() string
}
