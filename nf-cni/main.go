package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
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

	fmt.Fprintf(fp, "conf = %+v", conf)

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

func cmdAdd(args *skel.CmdArgs) error {
	fp, _ := os.OpenFile("/tmp/cni_debug", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	defer fp.Close()

	conf, err := parseNetConf(args.StdinData)
	if err != nil {
		return err
	}

	if conf.DeviceID != "" {
		fmt.Fprintf(fp, "conf.DeviceID = %+v", conf.DeviceID)
		link, err := netlink.LinkByName(conf.DeviceID)
		if err != nil {
			return fmt.Errorf("failed to lookup master %q: %v", conf.DeviceID, err)
		}
		link.Attrs()
	} else {
		return fmt.Errorf("LoadConf(): pci addr is required")
	}

	netns, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("failed to open netns %q: %v", netns, err)
	}
	defer netns.Close()

	fmt.Fprintf(fp, "args.Netns = %+v", args.Netns)

	var result types.Result

	result = &current.Result{
		CNIVersion: conf.CNIVersion,
		Interfaces: []*current.Interface{
			{
				Name:    args.IfName,
				Mac:     "00:00:00:00:00:00",
				Sandbox: args.Netns,
			},
		},
	}

	return types.PrintResult(result, conf.CNIVersion)
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

