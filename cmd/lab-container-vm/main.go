// lab-container-vm is installed only in an operator-approved, single-workload guest image.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/OutboundSpade/LabCentral/pkg/containervm"
)

const directory = "/var/lib/labcentral-container-vm"
const configPath = directory + "/config.json"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "container runtime operation failed")
		os.Exit(1)
	}
}
func podman(args ...string) ([]byte, error) {
	command := exec.Command("/usr/sbin/runuser", append([]string{"-u", "labworkload", "--", "/usr/bin/podman"}, args...)...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/home/labworkload", "XDG_RUNTIME_DIR=/run/user/1001"}
	// Podman's inspect/create output is bounded by this contract; guest output never becomes provider authority.
	return command.Output()
}
func run() error {
	if os.Geteuid() != 0 || len(os.Args) < 2 {
		return fmt.Errorf("invalid invocation")
	}
	if os.Args[1] == "run" && len(os.Args) == 2 {
		if _, err := os.ReadFile(configPath); err != nil {
			return err
		}
		cmd := exec.Command("/usr/sbin/runuser", "-u", "labworkload", "--", "/usr/bin/podman", "start", "--attach", "workload")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/home/labworkload", "XDG_RUNTIME_DIR=/run/user/1001"}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	if os.Args[1] != "ensure" || len(os.Args) != 3 || len(os.Args[2]) > containervm.MaximumBytes*2 {
		return fmt.Errorf("invalid invocation")
	}
	data, err := base64.RawStdEncoding.DecodeString(os.Args[2])
	if err != nil {
		return err
	}
	c, err := containervm.Decode(data)
	if err != nil {
		return err
	}
	data, _ = json.Marshal(c)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(directory+"/lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	existing, err := os.ReadFile(configPath)
	if err == nil && !bytes.Equal(existing, data) {
		return fmt.Errorf("runtime already bound")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		f, e := os.CreateTemp(directory, ".config-")
		if e != nil {
			return e
		}
		defer os.Remove(f.Name())
		if _, e = f.Write(data); e != nil {
			f.Close()
			return e
		}
		if e = f.Sync(); e != nil {
			f.Close()
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
		if e = os.Rename(f.Name(), configPath); e != nil {
			return e
		}
	}
	sum := sha256.Sum256(data)
	binding := hex.EncodeToString(sum[:])
	label, inspectErr := podman("inspect", "--format", "{{ index .Config.Labels \"io.labcentral.binding\" }}", "workload")
	if inspectErr != nil {
		if _, err = podman(c.PodmanCreateArgs(binding)...); err != nil {
			return err
		}
	} else if string(bytes.TrimSpace(label)) != binding {
		return fmt.Errorf("container binding mismatch")
	}
	if err = exec.Command("/usr/bin/systemctl", "start", "labcentral-container.service").Run(); err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		state, e := podman("inspect", "--format", "{{.State.Running}}", "workload")
		if e == nil && string(bytes.TrimSpace(state)) == "true" {
			fmt.Println(`{"schemaVersion":"labcentral.container-vm/v1","ready":true}`)
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("workload not running")
}
