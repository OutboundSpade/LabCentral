package dcv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCPUDesktopDisablesGLAndMustHaveRunningDisplay(t *testing.T) {
	creates, reads := 0, 0
	run := func(ctx context.Context, command string) ([]byte, error) {
		if command == warmGraphics {
			return nil, nil
		}
		if strings.Contains(command, "create-session") {
			if command != "sudo dcv create-session --type virtual --gl off --user student --owner student student" {
				t.Fatalf("CPU virtual desktop must disable DCV-GL while retaining its user and owner: %s", command)
			}
			creates++
			return nil, nil
		}
		reads++
		if reads == 1 {
			return []byte(`[{"id":"student","status":"running","x11-display":""}]`), nil
		}
		return []byte(`[{"id":"student","status":"running","x11-display":":1"}]`), nil
	}
	if err := start(context.Background(), run, "student", "student", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || reads != 2 {
		t.Fatal(creates, reads)
	}
}
func TestFailedLaunchRetriesOnlyConfirmedAbsence(t *testing.T) {
	for _, outcome := range []string{"absent", "unknown", "starting"} {
		t.Run(outcome, func(t *testing.T) {
			creates := 0
			run := func(ctx context.Context, command string) ([]byte, error) {
				if command == warmGraphics {
					return nil, nil
				}
				if strings.Contains(command, "create-session") {
					creates++
					return nil, nil
				}
				if creates == 2 {
					return []byte(`[{"id":"student","status":"running","x11-display":":1"}]`), nil
				}
				if outcome == "unknown" {
					return nil, errors.New("response lost")
				}
				if outcome == "starting" {
					return []byte(`[{"id":"student","status":"starting"}]`), nil
				}
				return []byte(`[]`), nil
			}
			err := start(context.Background(), run, "student", "student", 5*time.Millisecond, time.Millisecond)
			if outcome == "absent" {
				if err != nil || creates != 2 {
					t.Fatal(err, creates)
				}
			} else if err == nil || creates != 1 {
				t.Fatal(err, creates)
			}
		})
	}
}
func TestCancellationDoesNotRestartSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	creates := 0
	run := func(_ context.Context, command string) ([]byte, error) {
		if command == warmGraphics {
			return nil, nil
		}
		creates++
		cancel()
		return nil, context.Canceled
	}
	if err := start(ctx, run, "student", "student", time.Second, time.Millisecond); !errors.Is(err, context.Canceled) || creates != 1 {
		t.Fatal(err, creates)
	}
}

func TestEmptyStartupStatusWaitsWithoutDuplicatingSession(t *testing.T) {
	for _, eventuallyReady := range []bool{true, false} {
		t.Run(fmt.Sprint(eventuallyReady), func(t *testing.T) {
			creates, reads := 0, 0
			run := func(_ context.Context, stringCommand string) ([]byte, error) {
				if stringCommand == warmGraphics {
					return nil, nil
				}
				if strings.Contains(stringCommand, "create-session") {
					creates++
					return nil, nil
				}
				reads++
				if eventuallyReady && reads > 2 {
					return []byte(`[{"id":"student","status":"running","x11-display":":1"}]`), nil
				}
				return []byte(" \n"), nil
			}
			err := start(context.Background(), run, "student", "student", 10*time.Millisecond, time.Millisecond)
			if creates != 1 || reads < 3 {
				t.Fatal(creates, reads)
			}
			if eventuallyReady && err != nil || !eventuallyReady && !errors.Is(err, errEmptyStatus) {
				t.Fatal(err)
			}
		})
	}
	ready, err := Ready(context.Background(), func(context.Context, string) ([]byte, error) { return nil, nil }, "student")
	if ready || !errors.Is(err, errEmptyStatus) {
		t.Fatal("empty output was treated as confirmed absence", ready, err)
	}
}

func TestGraphicsPreparationFailureDoesNotCreateSession(t *testing.T) {
	for _, cause := range []error{errors.New("library read failed"), context.Canceled, context.DeadlineExceeded} {
		calls := 0
		err := start(context.Background(), func(_ context.Context, command string) ([]byte, error) {
			calls++
			if command != warmGraphics {
				t.Fatal("created a session after graphics preparation failed")
			}
			return nil, cause
		}, "student", "student", time.Second, time.Millisecond)
		if !errors.Is(err, cause) || calls != 1 {
			t.Fatal(err, calls)
		}
	}
}

// Exercise the actual pipeline: a failed read in either its producer or consumer
// must fail preparation, rather than being hidden by awk/sort or a subshell.
func TestGraphicsWarmupPropagatesModuleAndDependencyFailures(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	module, dependency := filepath.Join(dir, "Xdcv"), filepath.Join(dir, "dependency.so")
	for _, file := range []string{module, dependency} {
		if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	shims := map[string]string{
		"sudo": "#!/bin/sh\nexec \"$@\"\n",
		"ldd":  "#!/bin/sh\nprintf 'libfixture => %s (0x0)\\n' \"$LC_TEST_DEPENDENCY\"\n",
		"cat":  "#!/bin/sh\nif test \"$1\" = \"${LC_TEST_FAIL_FILE:-}\"; then exit 17; fi\nexec /bin/cat \"$@\"\n",
	}
	for name, content := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	command := strings.ReplaceAll(warmGraphics, "/usr/bin/Xdcv", module)
	for _, path := range []string{"/usr/lib/xorg/modules/extensions/libglx.so", "/usr/lib/*/libGLX_mesa.so.*", "/usr/lib/*/libgallium*.so", "/usr/lib/*/dri/swrast_dri.so"} {
		command = strings.ReplaceAll(command, path, filepath.Join(dir, "missing"))
	}
	for _, fail := range []string{"", module, dependency} {
		c := exec.Command("bash", "-c", command)
		c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "LC_TEST_DEPENDENCY="+dependency, "LC_TEST_FAIL_FILE="+fail)
		out, err := c.CombinedOutput()
		if (err != nil) != (fail != "") {
			t.Fatalf("fail=%q: %v: %s", fail, err, out)
		}
	}
}
