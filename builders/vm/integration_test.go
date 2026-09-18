package vm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
)

// Opt-in Linux/KVM acceptance test. The operator supplies a pinned profile,
// matching worker executable, and a persistent scratch directory. No production
// state or provider credentials are used. The test kills only its worker process
// and proves the independently supervised guest survives and is recovered.
func TestVMWorkerProcessLossRecovery(t *testing.T) {
	profilePath, binary, scratch := os.Getenv("LABCENTRAL_VM_TEST_PROFILE"), os.Getenv("LABCENTRAL_VM_TEST_BINARY"), os.Getenv("LABCENTRAL_VM_TEST_SCRATCH")
	if profilePath == "" || binary == "" || scratch == "" {
		t.Skip("requires explicit isolated KVM worker test configuration")
	}
	profile, profileDigest, err := LoadProfile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidatePersistentStorage(scratch); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	workerDigest := hex.EncodeToString(h.Sum(nil))
	dir, err := os.MkdirTemp(scratch, "worker-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("private evidence directory:", dir)
	serverFiles, clientFiles, _ := workerTLSFixture(t, dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	root := filepath.Join(dir, "state")
	log, err := os.OpenFile(filepath.Join(dir, "worker.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	var process *exec.Cmd
	start := func() {
		process = exec.Command(binary, "worker", "--state", root, "--profile", profilePath, "--endpoint", address, "--tls-ca", serverFiles.CA, "--tls-cert", serverFiles.Certificate, "--tls-key", serverFiles.Key)
		process.Stdout, process.Stderr = log, log
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
	}
	stop := func() {
		if process != nil {
			_ = process.Process.Kill()
			_ = process.Wait()
			process = nil
		}
	}
	defer stop()
	start()
	relay, err := NewRelay(address, clientFiles, "dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	waitHealthy := func() {
		for {
			probe, done := context.WithTimeout(ctx, time.Second)
			_, err := relay.Health(probe, &wire.HealthRequest{})
			done()
			if err == nil {
				return
			}
			if ctx.Err() != nil {
				t.Fatal("worker did not recover", ctx.Err())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitHealthy()
	request := workerRequest(t, relay.artifacts, "inv.real-recovery")
	request.Limits.TimeoutSeconds = 240
	request.Limits.MemoryBytes = 2 << 30
	request.Limits.CpuMillis = 2000
	request.Limits.MaximumOutputBytes = 16 << 20
	request.Limits.AllowedEgress = []string{}
	request.BindingConfiguration.CanonicalJson, _ = json.Marshal(WorkerBinding{ProfileSHA256: profileDigest, WorkerSHA256: workerDigest})
	stream, err := relay.builder.Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	var journal buildJournal
	journalPath := filepath.Join(root, request.Context.InvocationId, "journal.json")
	for {
		data, readErr := os.ReadFile(journalPath)
		if readErr == nil && json.Unmarshal(data, &journal) == nil && journal.Spec.Unit != "" {
			active, _ := unitActive(ctx, journal.Spec.Unit)
			if active {
				break
			}
		}
		if ctx.Err() != nil {
			t.Fatal("guest never launched")
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := StopGuest(cleanup, journal.Spec); err != nil {
			t.Error(err)
		}
	}()
	if journal.Spec.Limits.MemoryBytes+profile.MemoryOverheadBytes > profile.MaximumMemoryBytes {
		t.Fatal("invalid reservation")
	}
	originalDeadline := journal.Spec.Deadline
	stop() // SIGKILL: no graceful cleanup; QEMU must remain externally supervised.
	if active, err := unitActive(ctx, journal.Spec.Unit); err != nil || !active {
		t.Fatal("worker loss killed supervised guest", err)
	}
	start()
	waitHealthy()
	request.Context.FencingToken++
	var result *wire.BuilderServiceBuildResponse
	for {
		saved, err := relay.GetInvocation(ctx, &wire.BuilderServiceGetInvocationRequest{Context: request.Context})
		if err != nil {
			t.Fatal(err)
		}
		if saved.Result.State != "running" {
			result = saved.Result
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(time.Second)
	}
	if result.State != "succeeded" || result.Image.GetMediaType() != OCIMedia {
		t.Fatal("recovered build failed", result)
	}
	data, err := os.ReadFile(journalPath)
	if err != nil || json.Unmarshal(data, &journal) != nil || journal.Spec.Deadline != originalDeadline {
		t.Fatal("recovery changed original deadline", err)
	}
	if active, err := unitActive(ctx, journal.Spec.Unit); err != nil || active {
		t.Fatal("successful guest not closed", err)
	}
	read, err := relay.artifacts.Read(ctx, &wire.ArtifactTransferServiceReadRequest{Context: request.Context, Artifact: result.Image})
	if err != nil {
		t.Fatal(err)
	}
	h.Reset()
	var size uint64
	for {
		chunk, err := read.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		h.Write(chunk.Chunk)
		size += uint64(len(chunk.Chunk))
	}
	if size != result.Image.Size || hex.EncodeToString(h.Sum(nil)) != result.Image.Digest {
		t.Fatal("transferred output identity changed")
	}
	// With no worker goroutine supervising this guest, systemd must still
	// enforce the original wall-clock deadline and kill the whole guest unit.
	watchdogDir := filepath.Join(dir, "watchdog")
	if err := os.Mkdir(watchdogDir, 0700); err != nil {
		t.Fatal(err)
	}
	watchdog := RunSpec{Directory: watchdogDir, Unit: journal.Spec.Unit + "-deadline", ProxyPort: 1, Deadline: time.Now().Add(5 * time.Second), Limits: &wire.BuildLimits{CpuMillis: 1000, MemoryBytes: 512 << 20}}
	runner := Runner{Profile: profile, Executable: binary}
	if err := runner.launch(ctx, watchdog); err != nil {
		t.Fatal(err)
	}
	defer StopGuest(context.Background(), watchdog)
	for time.Now().Before(watchdog.Deadline.Add(15 * time.Second)) {
		active, err := unitActive(ctx, watchdog.Unit)
		if err != nil {
			t.Fatal(err)
		}
		if !active {
			if time.Now().Before(watchdog.Deadline.Add(-2 * time.Second)) {
				t.Fatal("watchdog guest exited before its deadline")
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("external guest deadline was not enforced")
}
