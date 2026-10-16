package hosted

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/internal/windows"
	"github.com/coder/websocket"
)

// This opt-in harness opens no infrastructure: use a disposable, ready Windows
// instance in an explicitly supplied private state directory. It uses the real
// hosted coach handler and instructor runner, with local identity fixtures only.
func TestLiveWindowsCoachAndStep(t *testing.T) {
	root := os.Getenv("LABCENTRAL_TEST_WINDOWS_STATE")
	if root == "" {
		t.Skip("requires an explicitly configured disposable Windows state directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine, err := lc.Open(root, sshOnlyProvider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	instances, err := engine.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ready := instances[:0]
	for _, inst := range instances {
		if inst.Status == "ready" {
			ready = append(ready, inst)
		}
	}
	instances = ready
	if len(instances) != 1 {
		t.Fatal("expected one ready disposable instance")
	}
	inst := instances[0]
	if inst.Status != "ready" || len(inst.Plan.Machines) != 1 || inst.Plan.Machines[0].OS != "windows" {
		t.Fatal("expected one ready Windows machine")
	}
	m := inst.Plan.Machines[0]
	s, v, _ := serviceFor(t)
	v.User.Admin = true
	if err = s.Store.Put(ctx, "session", v.ID, v); err != nil {
		t.Fatal(err)
	}
	teams := setupMeeting(t, s.Store)
	if _, err = s.Store.DB.ExecContext(ctx, `UPDATE teams SET status='ready',instance_id=? WHERE id=?`, inst.ID, teams[0].ID); err != nil {
		t.Fatal(err)
	}
	team := teams[0]
	team.InstanceID = inst.ID
	if err = s.Store.Put(ctx, "source", "", inst.Plan.Source); err != nil {
		t.Fatal(err)
	}
	s.Engine = engine
	app := httptest.NewUnstartedServer(nil)
	s.Config.Origin = "https://" + app.Listener.Addr().String()
	app.Config.Handler = s.Handler()
	app.StartTLS()
	defer app.Close()
	connection, _, err := websocket.Dial(ctx, app.URL+"/api/admin/teams/"+team.ID+"/machines/"+m.Name+"/terminal", &websocket.DialOptions{HTTPClient: app.Client(), HTTPHeader: http.Header{"Origin": {s.Config.Origin}, "Cookie": {"__Host-labcentral=cookie"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err = connection.Write(ctx, websocket.MessageText, []byte(`{"csrf":"csrf","cols":100,"rows":30}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := connection.Read(ctx)
	if err != nil || !strings.Contains(string(data), `"ready"`) {
		t.Fatal("coach readiness", string(data), err)
	}
	t.Log("real hosted coach terminal ready")
	alive := make(chan struct{}, 1)
	go func() {
		output := ""
		for {
			_, data, err := connection.Read(ctx)
			if err != nil {
				return
			}
			output += string(data)
			if len(output) > 4096 {
				output = output[len(output)-4096:]
			}
			if strings.Contains(output, "coach-alive") {
				select {
				case alive <- struct{}{}:
				default:
				}
			}
		}
	}()
	step := GuestPlaybook{Machine: m.Name, Script: "probe.ps1", TimeoutSeconds: 10}
	bundle := InstructorBundle{Version: 3, Files: []lc.File{{Path: "probe.ps1", Content: []byte("[IO.File]::WriteAllText((Join-Path $env:LABCENTRAL_STEP_DIR 'probe.txt'),'ok')\nexit 0")}}}
	start := time.Now()
	err = s.runGuestPlaybooks(ctx, team, inst.Plan.Source, bundle, []GuestPlaybook{step})
	t.Logf("hosted step with terminal open: %v (%s)", err, time.Since(start))
	if err != nil {
		t.Fatal(err)
	}
	bundle.Files[0].Content = []byte("exit 7")
	if err = s.runGuestPlaybooks(ctx, team, inst.Plan.Source, bundle, []GuestPlaybook{step}); !errors.Is(err, errPlaybookFailed) {
		t.Fatal("author failure beside terminal", err)
	}
	bundle.Files[0].Content = []byte("exit 0")
	if err = s.runGuestPlaybooks(ctx, team, inst.Plan.Source, bundle, []GuestPlaybook{step}); err != nil {
		t.Fatal("subsequent step beside terminal", err)
	}
	runLiveWindowsNativeTests(t, ctx, engine, inst, m.Name)
	// The literal marker is absent from echoed input, so this proves the shell
	// still executes commands after the steps and native launcher tests.
	if err = connection.Write(ctx, websocket.MessageBinary, []byte("Write-Output ('coach-'+'alive')\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-alive:
		t.Log("coach terminal still executes commands after guest steps")
	case <-ctx.Done():
		t.Fatal("coach terminal stopped responding")
	}
}

// Infrastructure mutations are deliberately unavailable in this SSH harness.
type sshOnlyProvider struct{}

func (sshOnlyProvider) Execute(context.Context, lc.Command, lc.Observer) (lc.Result, error) {
	return lc.Result{}, fmt.Errorf("infrastructure mutation unavailable in SSH harness")
}

func runLiveWindowsNativeTests(t *testing.T, ctx context.Context, engine *lc.Engine, inst lc.Instance, machine string) {
	t.Helper()
	path := os.Getenv("LABCENTRAL_TEST_WINDOWS_BINARY")
	if path == "" {
		return
	}
	// Optional guest path of a cross-compiled internal/windows test binary,
	// copied to this disposable guest beforehand (for example with SFTP).
	err := engine.WithSSHMaterial(ctx, inst.ID, machine, func(_ lc.Instance, m lc.PlannedMachine, res lc.Resource, key []byte) error {
		dialer, err := lc.MachineDialer(lc.InstanceGateway(inst), key)
		if err != nil {
			return err
		}
		defer dialer.Close()
		client, err := dialer.Dial(ctx, res.Address, m.SSHUser(), key, m.HostPublicKey)
		if err != nil {
			return err
		}
		defer client.Close()
		target := "'" + strings.ReplaceAll(path, "'", "''") + "'"
		command := windows.Command(`$env:LABCENTRAL_TEST_WINDOWS_POWERSHELL="$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"; & ` + target + ` '-test.v' '-test.run' '^TestNativeWindows(StepFailureTimeoutLockAndCleanup|DefaultShellExitStatus)$'; exit $LASTEXITCODE`)

		out, err := remote.Run(ctx, client, command)
		t.Logf("native Windows tests beside coach terminal:\n%s", out)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
