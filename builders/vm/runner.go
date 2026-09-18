package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"google.golang.org/protobuf/proto"
)

type RunSpec struct {
	Directory string            `json:"directory"`
	Unit      string            `json:"unit"`
	ProxyPort uint16            `json:"proxyPort"`
	Deadline  time.Time         `json:"deadline"`
	Recipe    *wire.BuildRecipe `json:"recipe"`
	Limits    *wire.BuildLimits `json:"limits"`
}
type RunOutput struct {
	Digest      string    `json:"digest"`
	Size        uint64    `json:"size"`
	MediaType   string    `json:"mediaType"`
	CompletedAt time.Time `json:"completedAt"`
}
type Runner struct {
	Profile    Profile
	Executable string
}

func (r Runner) Run(ctx context.Context, spec RunSpec, start bool) (RunOutput, error) {
	var output RunOutput
	if err := ValidatePersistentStorage(spec.Directory); err != nil {
		return output, err
	}
	// A receipt is written only after the entire output passed host verification.
	if data, err := os.ReadFile(filepath.Join(spec.Directory, "output-receipt.json")); err == nil {
		var receipt RunOutput
		if len(data) > 4096 || strictJSON(data, &receipt) != nil || receipt.CompletedAt.IsZero() || receipt.CompletedAt.After(spec.Deadline) || receipt.Size == 0 || receipt.Size > spec.Limits.GetMaximumOutputBytes() || !digestValid(receipt.Digest) {
			return output, fmt.Errorf("invalid completed output receipt")
		}
		name := filepath.Join(spec.Directory, "output.next")
		if _, err := os.Stat(name); os.IsNotExist(err) {
			name = filepath.Join(spec.Directory, "output")
		}
		if err := verifyArtifactFile(name, receipt.Digest, receipt.Size); err != nil {
			return output, err
		}
		cleanupCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		if err := finalizeOutput(cleanupCtx, spec); err != nil {
			return output, err
		}
		return receipt, nil
	} else if !os.IsNotExist(err) {
		return output, err
	}
	if err := ValidateRecipe(spec.Recipe, spec.Limits); err != nil {
		return output, err
	}
	for _, host := range spec.Limits.AllowedEgress {
		if !slices.Contains(r.Profile.AllowedEgress, host) {
			return output, fmt.Errorf("recipe egress exceeds worker policy")
		}
	}
	if !filepath.IsAbs(spec.Directory) || !strings.HasPrefix(spec.Unit, "labcentral-build-") || !safeName.MatchString(spec.Unit) || spec.ProxyPort == 0 || time.Until(spec.Deadline) <= 0 {
		return output, fmt.Errorf("invalid disposable guest reservation")
	}
	runCtx, cancel := context.WithDeadline(ctx, spec.Deadline)
	defer cancel()
	proxy, err := NewEgressProxy(spec.Limits.AllowedEgress, int64(r.Profile.EgressBytes))
	if err != nil {
		return output, err
	}
	defer proxy.Close()
	proxy.charge, err = durableEgressBudget(spec.Directory, r.Profile.EgressBytes)
	if err != nil {
		return output, err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ProxyPort))))
	if err != nil {
		return output, err
	}
	proxyContext, proxyCancel := context.WithCancel(runCtx)
	defer proxyCancel()
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return proxyContext }}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = StopGuest(cleanupCtx, spec)
	}()
	if start {
		if err = r.launch(runCtx, spec); err != nil {
			return output, err
		}
	} else if active, checkErr := unitActive(runCtx, spec.Unit); checkErr != nil || !active {
		return output, fmt.Errorf("disposable build guest no longer running")
	}
	// A canceled or expired caller can never publish an output. The independent
	// systemd deadline also stops QEMU if this worker process disappears.

	var channel *GuestChannel
	connect := func() error {
		if channel != nil {
			channel.Close()
		}
		c, err := ConnectGuest(guestSocketPath(spec))
		if err != nil {
			return err
		}
		channel = c
		return nil
	}
	defer func() {
		if channel != nil {
			channel.Close()
		}
	}()
	for {
		if runCtx.Err() != nil {
			return output, runCtx.Err()
		}
		if err = connect(); err == nil {
			_, err = channel.Call(GuestRequest{Method: "status"})
			if err == nil {
				break
			}
		}
		if active, checkErr := unitActive(runCtx, spec.Unit); checkErr != nil || !active {
			return output, fmt.Errorf("disposable guest failed during boot")
		}
		select {
		case <-runCtx.Done():
			return output, runCtx.Err()
		case <-time.After(time.Second):
		}
	}
	recipe := proto.Clone(spec.Recipe).(*wire.BuildRecipe)
	for _, a := range recipe.Inputs {
		a.TransferToken = ""
	}
	for _, d := range recipe.Dependencies {
		d.Artifact.TransferToken = ""
	}
	response, err := channel.Call(GuestRequest{Method: "configure", Recipe: recipe, Limits: spec.Limits})
	if err != nil {
		return output, err
	}
	if response.State == "staging" {
		artifacts := append([]*wire.ArtifactHandle{}, recipe.Inputs...)
		for _, d := range recipe.Dependencies {
			artifacts = append(artifacts, d.Artifact)
		}
		for _, artifact := range artifacts {
			if err = uploadGuest(channel, filepath.Join(spec.Directory, "inputs", artifact.Digest), artifact); err != nil {
				return output, err
			}
		}
		if _, err = channel.Call(GuestRequest{Method: "start"}); err != nil {
			return output, err
		}
	}
	for {
		if runCtx.Err() != nil {
			return output, runCtx.Err()
		}
		response, err = channel.Call(GuestRequest{Method: "status"})
		if err != nil {
			if active, checkErr := unitActive(runCtx, spec.Unit); checkErr != nil || !active {
				return output, fmt.Errorf("disposable guest stopped")
			}
			if err = connect(); err != nil {
				return output, err
			}
			continue
		}
		if response.State == "succeeded" {
			break
		}
		if response.State != "running" {
			return output, fmt.Errorf("guest build stopped in state %.32q", response.State)
		}
		select {
		case <-runCtx.Done():
			return output, runCtx.Err()
		case <-time.After(time.Second):
		}
	}
	expectedMedia := OCIMedia
	if recipe.Configuration.Schema.Name == AnsibleSchema || recipe.Configuration.Schema.Name == containervm.RuntimeBuildSchema {
		expectedMedia = QcowMedia
	}
	if !digestValid(response.Digest) || response.Size == 0 || response.Size > spec.Limits.MaximumOutputBytes || response.MediaType != expectedMedia {
		return output, fmt.Errorf("invalid guest output declaration")
	}
	f, err := os.OpenFile(filepath.Join(spec.Directory, "output.next"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return output, err
	}
	defer f.Close()
	hash := sha256.New()
	var written uint64
	for written < response.Size {
		chunk, err := channel.Call(GuestRequest{Method: "read", Offset: written})
		if err != nil {
			return output, err
		}
		if len(chunk.Chunk) == 0 || uint64(len(chunk.Chunk)) > response.Size-written {
			return output, fmt.Errorf("invalid guest output chunk")
		}
		if _, err = io.MultiWriter(f, hash).Write(chunk.Chunk); err != nil {
			return output, err
		}
		written += uint64(len(chunk.Chunk))
		if runCtx.Err() != nil {
			return output, runCtx.Err()
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != response.Digest {
		return output, fmt.Errorf("guest output digest mismatch")
	}
	if err = f.Sync(); err != nil {
		return output, err
	}
	if err = f.Close(); err != nil {
		return output, err
	}
	if runCtx.Err() != nil {
		return output, runCtx.Err()
	}
	output = RunOutput{Digest: response.Digest, Size: response.Size, MediaType: response.MediaType, CompletedAt: time.Now().UTC()}
	if err = durableJSON(spec.Directory, "output-receipt.json", output); err != nil {
		return RunOutput{}, err
	}
	if err = finalizeOutput(runCtx, spec); err != nil {
		return RunOutput{}, err
	}
	return output, nil

}

// Idempotent completion also covers a crash after shutdown but before the
// invocation journal commits success. The output receipt is host-owned.
func finalizeOutput(ctx context.Context, spec RunSpec) error {
	if err := StopGuest(ctx, spec); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(spec.Directory, "disk.qcow2")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(filepath.Join(spec.Directory, "inputs")); err != nil {
		return err
	}
	pending := filepath.Join(spec.Directory, "output.next")
	if _, err := os.Stat(pending); err == nil {
		if err = os.Rename(pending, filepath.Join(spec.Directory, "output")); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir, err := os.Open(spec.Directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func uploadGuest(channel *GuestChannel, path string, artifact *wire.ArtifactHandle) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buffer := make([]byte, channelChunkSize)
	var offset uint64
	for {
		n, readErr := f.Read(buffer)
		if n > 0 {
			if uint64(n) > artifact.Size-offset {
				return fmt.Errorf("input size changed")
			}
			if _, err = channel.Call(GuestRequest{Method: "upload", Digest: artifact.Digest, Offset: offset, Chunk: buffer[:n]}); err != nil {
				return err
			}
			offset += uint64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if offset != artifact.Size {
		return fmt.Errorf("input truncated")
	}
	return nil
}
func (r Runner) launch(ctx context.Context, spec RunSpec) error {
	if err := prepareGuestSocket(spec); err != nil {
		return err
	}
	if active, checkErr := unitActive(ctx, spec.Unit); checkErr != nil || active {
		return fmt.Errorf("build unit already exists")
	}
	disk := filepath.Join(spec.Directory, "disk.qcow2")
	cmd := exec.CommandContext(ctx, r.Profile.QEMUImage.Path, "create", "-f", "qcow2", "-F", "qcow2", "-b", r.Profile.GuestImage.Path, disk, strconv.FormatUint(r.Profile.GuestDiskBytes, 10))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create trusted toolchain overlay")
	}
	args, err := r.command(spec)
	if err != nil {
		return err
	}
	cmd = exec.CommandContext(ctx, "/usr/bin/systemd-run", args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("start bounded disposable guest")
	}
	return nil
}
func (r Runner) command(spec RunSpec) ([]string, error) {
	// QEMU's cmd guest forwarder uses a shell internally; its command consists
	// exclusively of this protected executable path and an allocated integer.
	if !filepath.IsAbs(r.Executable) || strings.ContainsAny(r.Executable, " \t\n\r'\"`$;&|<>()\\") {
		return nil, fmt.Errorf("unsafe worker executable path")
	}
	for _, value := range []string{spec.Directory, r.Profile.QEMU.Path, r.Profile.GuestImage.Path} {
		if strings.ContainsAny(value, ",\n\r\x00") {
			return nil, fmt.Errorf("unsafe QEMU path")
		}
	}
	seconds := int64(time.Until(spec.Deadline).Seconds())
	if seconds <= 0 || seconds > 7200 {
		return nil, fmt.Errorf("invalid guest deadline")
	}
	property := func(k, v string) string { return "--property=" + k + "=" + v }
	args := []string{"--user", "--quiet", "--collect", "--unit=" + spec.Unit, "--working-directory=" + spec.Directory, property("RuntimeMaxSec", strconv.FormatInt(seconds, 10)), property("MemoryMax", strconv.FormatUint(spec.Limits.MemoryBytes+r.Profile.MemoryOverheadBytes, 10)), property("CPUQuota", strconv.FormatUint(uint64(spec.Limits.CpuMillis)/10, 10)+"%"), property("TasksMax", "128"), property("LimitFSIZE", strconv.FormatUint(r.Profile.GuestDiskBytes, 10)), property("KillMode", "control-group"), property("TimeoutStopSec", "10"), property("NoNewPrivileges", "yes"), property("UMask", "0077"), property("StandardOutput", "null"), property("StandardError", "journal"), property("LogRateLimitIntervalSec", "60"), property("LogRateLimitBurst", "50"), "--", r.Profile.QEMU.Path,
		"-no-user-config", "-nodefaults", "-no-reboot", "-display", "none", "-machine", "q35,accel=kvm", "-cpu", "host", "-m", strconv.FormatUint(spec.Limits.MemoryBytes>>20, 10), "-smp", strconv.FormatUint(uint64((spec.Limits.CpuMillis+999)/1000), 10),
		"-drive", "file=" + filepath.Join(spec.Directory, "disk.qcow2") + ",format=qcow2,if=virtio,cache=none",
		"-device", "virtio-serial-pci", "-chardev", "socket,id=agent,path=" + guestSocketPath(spec) + ",server=on,wait=off", "-device", "virtserialport,chardev=agent,name=org.labcentral.build",
		"-netdev", "user,id=network,restrict=on,ipv6=off,guestfwd=tcp:10.0.2.100:3128-cmd:" + r.Executable + " proxy-stdio " + strconv.Itoa(int(spec.ProxyPort)), "-device", "virtio-net-pci,netdev=network",
	}
	return args, nil
}
func unitActive(ctx context.Context, unit string) (bool, error) {
	if !safeName.MatchString(unit) {
		return false, fmt.Errorf("invalid build unit")
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "--user", "show", "--property=ActiveState", "--value", unit+".service")
	data, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("cannot confirm guest state")
	}
	value := strings.TrimSpace(string(data))
	if !slices.Contains([]string{"active", "activating", "deactivating", "reloading", "inactive", "failed"}, value) {
		return false, fmt.Errorf("unknown guest state")
	}
	return value != "inactive" && value != "failed", nil
}
func StopGuest(ctx context.Context, spec RunSpec) error {
	if !strings.HasPrefix(spec.Unit, "labcentral-build-") || !safeName.MatchString(spec.Unit) {
		return fmt.Errorf("invalid owned build unit")
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "--user", "stop", spec.Unit+".service")
	_ = cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if active, checkErr := unitActive(ctx, spec.Unit); checkErr != nil || active {
		return fmt.Errorf("guest closure not confirmed")
	}
	if err := os.Remove(guestSocketPath(spec)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ProxyStdio is called only by the fixed QEMU guestfwd command. Each guest TCP
// connection obtains a separate loopback connection to its build's proxy.
func ProxyStdio(port uint16) error {
	if port == 0 {
		return fmt.Errorf("invalid proxy port")
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(os.Stdout, conn) }()
	_, err = io.Copy(conn, os.Stdin)
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
	<-done
	return err
}
