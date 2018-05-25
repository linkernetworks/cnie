package main

import (
	"encoding/json"
	"fmt"
	"net"
	"runtime"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ip"
	"github.com/containernetworking/plugins/pkg/ns"
)

const defaultBrName = "ovsbr0"

type NetConf struct {
	types.NetConf
	BrName string `json:"bridge"`
	MTU    int    `json:"mtu"`
	PNIC   string `json:"pNIC"`
}

func init() {
	// this ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()
}

func loadNetConf(bytes []byte) (*NetConf, string, error) {
	n := &NetConf{
		BrName: defaultBrName,
	}
	if err := json.Unmarshal(bytes, n); err != nil {
		return nil, "", fmt.Errorf("failed to load netconf: %v", err)
	}
	return n, n.CNIVersion, nil
}

func setupVeth(netns ns.NetNS, br *OVSSwitch, ifName string, mtu int) (*current.Interface, *current.Interface, error) {
	contIface := &current.Interface{}
	hostIface := &current.Interface{}

	err := netns.Do(func(hostNS ns.NetNS) error {
		// create the veth pair in the container and move host end into host netns
		hostVeth, containerVeth, err := ip.SetupVeth(ifName, mtu, hostNS)
		if err != nil {
			return err
		}
		contIface.Name = containerVeth.Name
		contIface.Mac = containerVeth.HardwareAddr.String()
		contIface.Sandbox = netns.Path()
		hostIface.Name = hostVeth.Name
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// connect host veth end to the bridge
	if err := br.addPort(contIface.Name); err != nil {
		return nil, nil, fmt.Errorf("failed to connect %q to bridge %v: %v", hostIface.Name, br.bridgeName, err)
	}

	return hostIface, contIface, nil
}

func setupBridge(n *NetConf) (*OVSSwitch, *current.Interface, error) {
	// create bridge if necessary
	ovs, err := NewOVSSwitch(n.BrName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create bridge %q: %v", n.BrName, err)
	}

	return ovs, &current.Interface{
		Name: n.BrName,
	}, nil
}

func cmdAdd(args *skel.CmdArgs) error {
	n, cniVersion, err := loadNetConf(args.StdinData)
	if err != nil {
		return err
	}

	br, brInterface, err := setupBridge(n)
	if err != nil {
		return err
	}

	if err := br.addPort(n.PNIC); err != nil {
		return err
	}

	netns, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("failed to open netns %q: %v", args.Netns, err)
	}
	defer netns.Close()

	// TODO run the IPAM plugin and get back the config to apply

	// Convert whatever the IPAM result was into the current Result type
	result, err := current.NewResultFromResult(nil)
	if err != nil {
		return err
	}

	hostInterface, containerInterface, err := setupVeth(netns, br, args.IfName, n.MTU)
	if err != nil {
		return err
	}

	result.Interfaces = []*current.Interface{brInterface, hostInterface, containerInterface}

	if err := netns.Do(func(_ ns.NetNS) error {
		contVeth, err := net.InterfaceByName(args.IfName)
		_ = contVeth
		if err != nil {
			return err
		}

		// TODO Add the IP to the interface
		// if err := ipam.ConfigureIface(args.IfName, result); err != nil {
		// 	return err
		// }

		// TODO Send a gratuitous arp
		// for _, ipc := range result.IPs {
		// 	if ipc.Version == "4" {
		// 		_ = arping.GratuitousArpOverIface(ipc.Address.IP, *contVeth)
		// 	}
		// }
		return nil
	}); err != nil {
		return err
	}

	// TODO Refetch the bridge since its MAC address may change when the first
	// veth is added or after its IP address is set
	// br, err = bridgeByName(n.BrName)
	// if err != nil {
	// 	return err
	// }

	return types.PrintResult(result, cniVersion)
}

func cmdDel(args *skel.CmdArgs) error {
	n, _, err := loadNetConf(args.StdinData)
	_ = n
	if err != nil {
		return err
	}

	if args.Netns == "" {
		return nil
	}

	return err
}

func main() {
	skel.PluginMain(cmdAdd, cmdDel, version.All)
}
