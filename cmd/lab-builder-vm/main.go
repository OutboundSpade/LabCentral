package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/builders/vm"
	pluginruntime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "proxy-stdio" {
		if len(os.Args) != 3 {
			return fmt.Errorf("proxy port required")
		}
		port, err := strconv.ParseUint(os.Args[2], 10, 16)
		if err != nil {
			return err
		}
		return vm.ProxyStdio(uint16(port))
	}
	if len(os.Args) > 1 && os.Args[1] == "guest" {
		agent, err := vm.NewGuestAgent("/var/lib/labcentral-build")
		if err != nil {
			return err
		}
		for {
			channel, err := os.OpenFile("/dev/virtio-ports/org.labcentral.build", os.O_RDWR, 0)
			if err == nil {
				_ = agent.Serve(channel)
				channel.Close()
			}
			// A host reconnect must retain the running invocation in this process.
			time.Sleep(time.Second)
		}

	}
	mode := "plugin"
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "worker" || args[0] == "inspect-profile") {
		mode = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("lab-builder-vm", flag.ContinueOnError)
	kind := flags.String("kind", "", "installed builder kind: ansible, dockerfile or container-runtime")
	address := flags.String("endpoint", "", "dedicated worker host:port (listen address in worker mode)")
	root := flags.String("state", "", "private worker state directory")
	profileFile := flags.String("profile", "", "pinned worker profile file")
	ca := flags.String("tls-ca", "", "worker CA certificate")
	cert := flags.String("tls-cert", "", "worker or relay certificate")
	key := flags.String("tls-key", "", "private worker or relay key")
	serverName := flags.String("tls-server-name", "", "expected worker TLS name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	file, err := os.Open(executable)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		return err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if mode == "inspect-profile" {
		_, identity, err := vm.LoadProfile(*profileFile)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(vm.WorkerBinding{ProfileSHA256: identity, WorkerSHA256: digest})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tlsFiles := vm.TLSFiles{CA: *ca, Certificate: *cert, Key: *key, ServerName: *serverName}
	if mode == "worker" {
		if !filepath.IsAbs(*root) {
			return fmt.Errorf("absolute worker state path required")
		}
		if err := os.MkdirAll(*root, 0700); err != nil {
			return err
		}
		if err := vm.ValidatePersistentStorage(*root); err != nil {
			return err
		}
		profile, identity, err := vm.LoadProfile(*profileFile)
		if err != nil {
			return err
		}
		service, err := vm.NewService(*root, vm.Runner{Profile: profile, Executable: executable}, identity, digest)
		if err != nil {
			return err
		}
		defer service.Close()
		return vm.ServeWorker(ctx, *address, tlsFiles, service)
	}
	relay, err := vm.NewRelay(*address, tlsFiles, *kind)
	if err != nil {
		return err
	}
	defer relay.Close()
	return pluginruntime.Serve(ctx, pluginruntime.Services{Info: &wire.PluginInfo{PluginId: strings.TrimSuffix(vm.PluginID, ".vm") + "." + *kind, PluginVersion: "0.1.0", BuildDigest: "sha256:" + digest, Categories: []string{"builder"}, BuilderKinds: []string{*kind}, SupportedBuilderProtocols: []*wire.ProtocolVersionRange{{Major: 1}}, Features: []string{"artifact-transfer-v1", "builder-dependencies-v1", "oci-layout-v1"}}, Runtime: relay, Builder: relay, Artifacts: relay})
}
