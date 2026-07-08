// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package unsafe

import (
	gocontext "context"
	"fmt"
	"os"
	"strings"

	"github.com/tencentcloud/CubeSandbox/Cubelet/cmd/cubecli/commands"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/networkagentclient"
	"github.com/urfave/cli/v2"
	"github.com/vishvananda/netlink"
)

const (
	tapDeviceNamePrefix = "z"
)

var DestroyTap = &cli.Command{
	Name:  "destroytap",
	Usage: "destroy all the host tap device",
	Flags: commands.NetworkAgentFlags(),
	Action: func(clictx *cli.Context) error {
		if !commands.AskForConfirm("will destroy ALL of tap device directly, continue only if you confirm", 3) {
			return nil
		}

		ctx, cancel := gocontext.WithTimeout(gocontext.Background(), clictx.Duration("timeout"))
		defer cancel()
		endpoint, err := commands.ResolveNetworkAgentEndpoint(clictx)
		if err != nil {
			return fmt.Errorf("resolve network-agent endpoint: %w", err)
		}
		naClient, err := networkagentclient.NewClient(endpoint)
		if err != nil {
			return fmt.Errorf("create network-agent client for %q: %w", endpoint, err)
		}

		listResp, err := naClient.ListNetworks(ctx, &networkagentclient.ListNetworksRequest{})
		if err != nil {
			return fmt.Errorf("list managed networks from network-agent: %w", err)
		}
		if !commands.AskForConfirm(fmt.Sprintf("release managed networks from network-agent, TOTAL %d, continue only if you confirm", len(listResp.Networks)), 3) {
			return nil
		}
		for _, network := range listResp.Networks {
			err := naClient.ReleaseNetwork(ctx, &networkagentclient.ReleaseNetworkRequest{
				SandboxID:     network.SandboxID,
				NetworkHandle: network.NetworkHandle,
			})
			if err != nil {
				return fmt.Errorf("release managed network sandbox=%s handle=%s: %w", network.SandboxID, network.NetworkHandle, err)
			}
			fmt.Printf("released managed network sandbox=%s handle=%s tap=%s\n", network.SandboxID, network.NetworkHandle, network.TapName)
		}

		taps, err := listHostTapLinks()
		if err != nil {
			return fmt.Errorf("list host tap devices: %w", err)
		}
		if !commands.AskForConfirm(fmt.Sprintf("destroy residual tap devices, TOTAL %d, continue only if you confirm", len(taps)), 3) {
			return nil
		}

		success := true
		for _, link := range taps {
			if err := netlink.LinkDel(link); err != nil {
				success = false
				fmt.Fprintf(os.Stderr, "failed to delete tap %q: %v\n", link.Attrs().Name, err)
			} else {
				fmt.Println(link.Attrs().Name)
			}
		}
		if !success {
			return fmt.Errorf("some ops error")
		}
		return nil
	},
}

func listHostTapLinks() ([]netlink.Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	result := make([]netlink.Link, 0)
	for _, link := range links {
		tap, ok := link.(*netlink.Tuntap)
		if !ok {
			continue
		}
		if tap.Mode != netlink.TUNTAP_MODE_TAP {
			continue
		}
		if !strings.HasPrefix(tap.Name, tapDeviceNamePrefix) {
			continue
		}
		result = append(result, link)
	}
	return result, nil
}
