package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/openshift/dpu-operator/dpu-cni/pkgs/cnitypes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	// SockDir is the default Kubelet device plugin socket directory
	SockDir = "/var/lib/kubelet/plugins_registry"
	// DeprecatedSockDir is the deprecated Kubelet device plugin socket directory
	DeprecatedSockDir = "/var/lib/kubelet/device-plugins"
)

const (
	// KubeEndPoint is kubelet socket name
	KubeEndPoint = "kubelet.sock"
)

// ExcludeConfig contains excluded list of devices
type ExcludeConfig struct {
	ExcludeDevices []string `json:"exclude"`
}

// ResourceFactory is an interface to get instances of ResourcePool and ResourceServer
type ResourceFactory interface {
	GetResourceServer(ResourcePool) (ResourceServer, error)
	GetResourcePool(rc *ExcludeConfig) (ResourcePool, error)
}

// ResourcePool represents a generic resource entity
type ResourcePool interface {
	// extended API for internal use
	GetResourceName() string
	GetResourcePrefix() string
	GetDevices() map[string]*pluginapi.Device // for ListAndWatch
	Probe() bool
	GetDeviceSpecs(deviceIDs []string) []*pluginapi.DeviceSpec
	GetEnvs(prefix string, deviceIDs []string) (map[string]string, error)
	GetMounts(deviceIDs []string) []*pluginapi.Mount
	StoreDeviceInfoFile(resourceNamePrefix string) error
	CleanDeviceInfoFile(resourceNamePrefix string) error
	GetCDIName() string
}

// ResourceServer is gRPC server implements K8s device plugin api
type ResourceServer interface {
	// Device manager API
	pluginapi.DevicePluginServer
	// grpc server related
	Start() error
	Stop() error
	// Init initializes resourcePool
	Init() error
	// Watch watches for socket file deletion and restart server if needed
	Watch()
}

// NadUtils is an interface for Network-Attachment-Definition utilities
type NadUtils interface {
}

const (
	VendorPluginSocketPath string = cnitypes.DaemonBaseDir + "vendor-plugin/vendor-plugin.sock"

	// Device plugin settings.
	pluginMountPath = "/var/lib/kubelet/device-plugins"
	kubeletEndpoint = "kubelet.sock"
	pluginEndpoint  = "sriovNet.sock"
	resourceName    = "openshift.io/dpu"
)

// sriovManager manages sriov networking devices
type nfResources struct {
	socketFile string
	devices    map[string]pluginapi.Device // for Kubelet DP API
	grpcServer *grpc.Server
	pluginapi.DevicePluginServer
	log logr.Logger
}

type DevicePlugin interface {
	Start() error
}

func (nf *nfResources) ListAndWatch(empty *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	changed := true
	for {
		if changed {
			resp := new(pluginapi.ListAndWatchResponse)
			for _, dev := range nf.devices {
				resp.Devices = append(resp.Devices, &pluginapi.Device{ID: dev.ID, Health: dev.Health})
			}
			fmt.Printf("ListAndWatch: send devices %v\n", resp)
			if err := stream.Send(resp); err != nil {
				fmt.Printf("Error. Cannot update device states: %v\n", err)
				nf.grpcServer.Stop()
				return err
			}
		}
		time.Sleep(5 * time.Second)
		changed = nf.Changed()
	}
}

func (nf *nfResources) Changed() bool {
	changed := false
	for id, dev := range nf.devices {
		state := nf.GetDeviceState(id)
		if dev.Health != state {
			changed = true
			dev.Health = state
			nf.devices[id] = dev
		}
	}
	return changed
}

// Allocate passes the dev name as an env variable to the requesting container
func (nf *nfResources) Allocate(ctx context.Context, rqt *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := new(pluginapi.AllocateResponse)
	devName := ""
	for _, container := range rqt.ContainerRequests {
		containerResp := new(pluginapi.ContainerAllocateResponse)
		for _, id := range container.DevicesIDs {
			fmt.Printf("DeviceID in Allocate: %v \n", id)
			dev, ok := nf.devices[id]
			if !ok {
				fmt.Printf("Error. Invalid allocation request with non-existing device %s", id)
			}
			if dev.Health != pluginapi.Healthy {
				fmt.Printf("Error. Invalid allocation request with unhealthy device %s", id)
			}

			devName = devName + id + ","
		}

		fmt.Printf("device(s) allocated: %s\n", devName)
		envmap := make(map[string]string)
		envmap["NF-DEV"] = devName

		containerResp.Envs = envmap
		resp.ContainerResponses = append(resp.ContainerResponses, containerResp)
	}
	return resp, nil
}

func (nf *nfResources) GetDeviceState(DeviceName string) string {
	// TODO: Discover device health
	return pluginapi.Healthy
}

func (nf *nfResources) Start() error {
	nf.cleanup()

	devices := []string{"eno12399v0", "eno12399v1", "eno12399v2", "eno12399v3"}

	for _, dev := range devices {
		nf.devices[dev] = pluginapi.Device{ID: dev, Health: pluginapi.Healthy}
	}

	for dev := range nf.devices {
		nf.log.Info(dev)
	}

	pluginEndpoint := filepath.Join(pluginapi.DevicePluginPath, nf.socketFile)
	fmt.Printf("Starting NF Device Plugin server at: %s\n", pluginEndpoint)
	lis, err := net.Listen("unix", pluginEndpoint)
	if err != nil {
		fmt.Printf("Error: Starting NF Device Plugin server failed: %v", err)
	}
	nf.grpcServer = grpc.NewServer()

	kubeletEndpoint := filepath.Join("unix:", DeprecatedSockDir, KubeEndPoint)

	conn, err := grpc.Dial(kubeletEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))

	if err != nil {
		fmt.Printf("%s device plugin unable connect to Kubelet : %v", resourceName, err)
		return err
	}
	defer conn.Close()

	pluginapi.RegisterDevicePluginServer(nf.grpcServer, nf)

	client := pluginapi.NewRegistrationClient(conn)

	go nf.grpcServer.Serve(lis)

	// Use connectWithRetry for the pluginEndpoint call
	conn, err = nf.connectWithRetry("unix:" + pluginEndpoint)
	if err != nil {
		fmt.Printf("error. unable to establish test connection with %s gRPC server: %v", resourceName, err)
		return err
	}
	fmt.Printf("%s device plugin endpoint started serving \n", resourceName)
	conn.Close()

	ctx := context.Background()

	request := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     nf.socketFile,
		ResourceName: resourceName,
	}

	if _, err = client.Register(ctx, request); err != nil {
		fmt.Printf("%s device plugin unable to register with Kubelet : %v \n", resourceName, err)
		return err
	}
	fmt.Printf("%s device plugin registered with Kubelet\n", resourceName)

	return nil
}

// connectWithRetry tries to establish a connection with the given endpoint, with retries.
func (nf *nfResources) connectWithRetry(endpoint string) (*grpc.ClientConn, error) {
	var conn *grpc.ClientConn
	var err error

	retryPolicy := `{
		"methodConfig": [{
		  "waitForReady": true,
		  "retryPolicy": {
			  "MaxAttempts": 40,
			  "InitialBackoff": "1s",
			  "MaxBackoff": "16s",
			  "BackoffMultiplier": 2.0,
			  "RetryableStatusCodes": [ "UNAVAILABLE" ]
		  }
		}]}`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err = grpc.DialContext(
		ctx,
		endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithDefaultServiceConfig(retryPolicy),
	)
	if err != nil {
		nf.log.Error(err, "Failed to establish connection with retry", "endpoint", endpoint)
		return nil, err
	}

	return conn, nil
}

func (nf *nfResources) cleanup() error {
	pluginEndpoint := filepath.Join(pluginapi.DevicePluginPath, nf.socketFile)
	if err := os.Remove(pluginEndpoint); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (nf *nfResources) PreStartContainer(ctx context.Context, psRqt *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

func (nf *nfResources) GetDevicePluginOptions(ctx context.Context, empty *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{
		PreStartRequired: false,
	}, nil
}

func NewGrpcPlugin() *nfResources {
	return &nfResources{
		log:        ctrl.Log.WithName("GrpcPlugin"),
		devices:    make(map[string]pluginapi.Device),
		socketFile: pluginEndpoint,
	}
}

func main() {
	dp := NewGrpcPlugin()
	err := dp.Start()
	if err != nil {
		dp.log.Error(err, "device plugin call failed")
	}

	// respond to syscalls for termination
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// Catch termination signals
	sig := <-sigCh
	dp.log.Info("Received signal \"%v\", shutting down.", sig)

}

