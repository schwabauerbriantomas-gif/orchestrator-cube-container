// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package container

import (
	gocontext "context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/tencentcloud/CubeSandbox/Cubelet/cmd/cubecli/commands"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/networkagentclient"
	"github.com/urfave/cli/v2"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	tapDeviceNamePrefix = "z"
)

type displayTap struct {
	Name        string
	IfIndex     int
	Used        bool
	InCubeVS    bool
	PortMapping []networkagentclient.PortMapping
}

var ListTapCommand = &cli.Command{
	Name:  "taps",
	Usage: "list all taps",
	Flags: commands.NetworkAgentFlags(),
	Action: func(clictx *cli.Context) error {
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
			return fmt.Errorf("list networks from network-agent: %w", err)
		}

		managedByIfindex := make(map[int]networkagentclient.NetworkState, len(listResp.Networks))
		managedByTapName := make(map[string]networkagentclient.NetworkState, len(listResp.Networks))
		for _, network := range listResp.Networks {
			if network.TapIfIndex != 0 {
				managedByIfindex[int(network.TapIfIndex)] = network
			}
			if network.TapName != "" {
				managedByTapName[network.TapName] = network
			}
		}

		links, err := netlink.LinkList()
		if err != nil {
			return fmt.Errorf("list netlink: %w", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 4, 8, 4, ' ', 0)
		fmt.Fprintln(w, "IP\tUSED\tINCUBEVS\tIFINDEX\tPORTMAPPING")
		var displayed []displayTap
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

			s := tap.Name[len(tapDeviceNamePrefix):]
			ip := net.ParseIP(s)
			if ip == nil {
				fmt.Fprintf(os.Stderr, "invalid ip %q, skip", tap.Name)
				continue
			}

			managed, incubevs := managedByIfindex[tap.Index]
			if !incubevs {
				managed, incubevs = managedByTapName[tap.Name]
			}
			pm := append([]networkagentclient.PortMapping(nil), managed.PortMappings...)
			sort.Slice(pm, func(i, j int) bool {
				if pm[i].ContainerPort == pm[j].ContainerPort {
					return pm[i].HostPort < pm[j].HostPort
				}
				return pm[i].ContainerPort < pm[j].ContainerPort
			})

			displayed = append(displayed, displayTap{
				Name:        s,
				IfIndex:     tap.Index,
				Used:        link.Attrs().RawFlags&unix.IFF_LOWER_UP > 0,
				InCubeVS:    incubevs,
				PortMapping: pm,
			})
		}

		sort.Slice(displayed, func(i, j int) bool {
			return displayed[i].Name < displayed[j].Name
		})

		for _, tap := range displayed {
			fmt.Fprintf(w, "%s\t%t\t%t\t%d\t%v\n",
				tap.Name, tap.Used, tap.InCubeVS, tap.IfIndex, displayPortMapping(tap.PortMapping))
		}

		return w.Flush()
	},
}

func displayPortMapping(m []networkagentclient.PortMapping) string {
	var s []string
	for _, p := range m {
		s = append(s, fmt.Sprintf("%d->%d", p.ContainerPort, p.HostPort))
	}
	return strings.Join(s, ",")
}
