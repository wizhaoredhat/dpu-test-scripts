package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ipam"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
)

// NetConf extends types.NetConf for dpu-sriov-cni
type NetConf struct {
	types.NetConf
	Tmp      string
	DeviceID string `json:"deviceID"` // PCI address of a VF in valid sysfs format
	LogLevel string `json:"logLevel,omitempty"`
	LogFile  string `json:"logFile,omitempty"`
}

func init() {
	// this ensures that main runs only on main thread (thread group leader).
	// since namespace ops (unshare, setns) are done for a single thread, we
	// must ensure that the goroutine does not jump from OS thread to thread
	runtime.LockOSThread()
}

func parseNetConf(bytes []byte) (*NetConf, error) {
	fp, _ := os.OpenFile("/tmp/cni_debug", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	defer fp.Close()

	conf := &NetConf{}
	if err := json.Unmarshal(bytes, conf); err != nil {
		return nil, fmt.Errorf("failed to parse network config: %v", err)
	}

	fmt.Fprintf(fp, "conf = %+v\n", conf)

	if conf.RawPrevResult != nil {
		if err := version.ParsePrevResult(&conf.NetConf); err != nil {
			return nil, fmt.Errorf("failed to parse prevResult: %v", err)
		}
		if _, err := current.NewResultFromResult(conf.PrevResult); err != nil {
			return nil, fmt.Errorf("failed to convert result to current version: %v", err)
		}
	}

	return conf, nil
}

func moveLinkInNetNamespace(hostDev netlink.Link, containerNs ns.NetNS, ifName string) (netlink.Link, error) {
	fp, _ := os.OpenFile("/tmp/cni_debug", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	defer fp.Close()

	origLinkFlags := hostDev.Attrs().Flags
	origHostDevName := hostDev.Attrs().Name

	// Get the default namespace from the host
	defaultHostNs, err := ns.GetCurrentNS()
	if err != nil {
		return nil, fmt.Errorf("failed to get host namespace: %v", err)
	}
	fmt.Fprintf(fp, "WZ DEBUG defaultHostNs = %+v\n", defaultHostNs)

	// Devices can be renamed only when down
	if err = netlink.LinkSetDown(hostDev); err != nil {
		return nil, fmt.Errorf("failed to set %s down: %v", hostDev.Attrs().Name, err)
	}
	fmt.Fprintf(fp, "WZ DEBUG Set link %s\n", hostDev.Attrs().Name)

	// Restore original link state in case of error
	defer func() {
		if err != nil {
			// If the device is originally up, make sure to bring it up
			if origLinkFlags&net.FlagUp == net.FlagUp && hostDev != nil {
				_ = netlink.LinkSetUp(hostDev)
			}
		}
	}()

	// Generate a temp name with the interface index
	tempName := fmt.Sprintf("%s%d", "temp_", hostDev.Attrs().Index)

	// Rename to tempName
	if err := netlink.LinkSetName(hostDev, tempName); err != nil {
		return nil, fmt.Errorf("failed to rename device %s to %s: %v", hostDev.Attrs().Name, tempName, err)
	}
	fmt.Fprintf(fp, "WZ DEBUG Rename link %s to %s\n", hostDev.Attrs().Name, tempName)

	// Get updated Link obj
	tempDev, err := netlink.LinkByName(tempName)
	if err != nil {
		return nil, fmt.Errorf("failed to find %s after rename to %s: %v", hostDev.Attrs().Name, tempName, err)
	}

	// Replace the link object of the renamed interface
	hostDev = tempDev

	// Restore original netdev name in case of error
	defer func() {
		if err != nil && hostDev != nil {
			_ = netlink.LinkSetName(hostDev, origHostDevName)
		}
	}()

	// Move interface to the container network namespace
	if err = netlink.LinkSetNsFd(hostDev, int(containerNs.Fd())); err != nil {
		return nil, fmt.Errorf("failed to move %s to container ns: %v", hostDev.Attrs().Name, err)
	}
	fmt.Fprintf(fp, "WZ DEBUG Move link %s to %s\n", hostDev.Attrs().Name, containerNs.Path())

	var contDev netlink.Link
	tempDevName := hostDev.Attrs().Name
	if err = containerNs.Do(func(_ ns.NetNS) error {
		var err error
		contDev, err = netlink.LinkByName(tempDevName)
		if err != nil {
			return fmt.Errorf("failed to find %q: %v", tempDevName, err)
		}

		// Move netdev back to host namespace in case of error
		defer func() {
			if err != nil {
				_ = netlink.LinkSetNsFd(contDev, int(defaultHostNs.Fd()))
				// we need to get updated link object as link was moved back to host namespace
				_ = defaultHostNs.Do(func(_ ns.NetNS) error {
					hostDev, _ = netlink.LinkByName(tempDevName)
					return nil
				})
			}
		}()

		// Save host device name into the container device's alias property
		if err = netlink.LinkSetAlias(contDev, origHostDevName); err != nil {
			return fmt.Errorf("failed to set alias to %q: %v", tempDevName, err)
		}
		fmt.Fprintf(fp, "WZ DEBUG Save original host name %s\n", origHostDevName)

		// Rename container device to respect ifName coming from CNI netconf
		if err = netlink.LinkSetName(contDev, ifName); err != nil {
			return fmt.Errorf("failed to rename device %q to %q: %v", tempDevName, ifName, err)
		}
		fmt.Fprintf(fp, "WZ DEBUG using netconf.ifName %s\n", ifName)

		// Restore tempDevName in case of error
		defer func() {
			if err != nil {
				_ = netlink.LinkSetName(contDev, tempDevName)
			}
		}()

		// Bring container device up
		if err = netlink.LinkSetUp(contDev); err != nil {
			return fmt.Errorf("failed to set %q up: %v", ifName, err)
		}

		// Bring device down in case of error
		defer func() {
			if err != nil {
				_ = netlink.LinkSetDown(contDev)
			}
		}()

		// Retrieve link again to get up-to-date name and attributes
		contDev, err = netlink.LinkByName(ifName)
		if err != nil {
			return fmt.Errorf("failed to find %q: %v", ifName, err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return contDev, nil
}

func cmdAdd(args *skel.CmdArgs) error {
	fp, _ := os.OpenFile("/tmp/cni_debug", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	defer fp.Close()

	conf, err := parseNetConf(args.StdinData)
	if err != nil {
		return err
	}

	containerNs, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("failed to open netns %+v: %v", containerNs, err)
	}
	defer containerNs.Close()

	fmt.Fprintf(fp, "WZ DEBUG args.Netns = %+v\n", args.Netns)

	result := &current.Result{}
	var contDev netlink.Link

	// TODO: In the future we may want to support the following formats coming from the device plugin
	// pciAddr: For Netdev and DPDK use cases
	// auxDevices: Device plugins may allocate network device on a bus different than PCI
	// Also this code would not work for DPDK interfaces.
	hostDev, err := netlink.LinkByName(conf.DeviceID)
	if err != nil {
		return fmt.Errorf("failed to find host device: %v", err)
	}

	contDev, err = moveLinkInNetNamespace(hostDev, containerNs, args.IfName)
	if err != nil {
		return fmt.Errorf("failed to move link %v", err)
	}

	result.Interfaces = []*current.Interface{{
		Name:    contDev.Attrs().Name,
		Mac:     contDev.Attrs().HardwareAddr.String(),
		Sandbox: containerNs.Path(),
	}}

	if conf.IPAM.Type == "" {
		return types.PrintResult(result, conf.CNIVersion)
	}

	// Run the IPAM plugin and get back the config to apply
	r, err := ipam.ExecAdd(conf.IPAM.Type, args.StdinData)
	if err != nil {
		return err
	}

	// Invoke ipam del if err to avoid ip leak
	defer func() {
		if err != nil {
			ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		}
	}()

	// Convert the IPAM result was into the current Result type
	newResult, err := current.NewResultFromResult(r)
	if err != nil {
		return err
	}

	if len(newResult.IPs) == 0 {
		return errors.New("IPAM plugin returned missing IP config")
	}

	for _, ipc := range newResult.IPs {
		// All addresses apply to the container interface
		ipc.Interface = current.Int(0)
	}

	newResult.Interfaces = result.Interfaces

	err = containerNs.Do(func(_ ns.NetNS) error {
		return ipam.ConfigureIface(args.IfName, newResult)
	})
	if err != nil {
		return err
	}

	newResult.DNS = conf.DNS

	return types.PrintResult(newResult, conf.CNIVersion)
}

func cmdDel(args *skel.CmdArgs) error {
	return nil
}

func cmdCheck(_ *skel.CmdArgs) error {
	return nil
}

func main() {
	skel.PluginMain(cmdAdd, cmdCheck, cmdDel, version.All, "")
}
