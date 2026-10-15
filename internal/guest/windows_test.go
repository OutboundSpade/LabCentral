package guest

import (
	"encoding/base64"
	"encoding/json"
	"github.com/OutboundSpade/LabCentral/v2/internal/pscheck"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

func TestWindowsBootstrapOwnershipReadinessAndFirewall(t *testing.T) {
	m := lc.PlannedMachine{Name: "server", PublicKey: "ssh-ed25519 public", HostPrivateKey: "private", HostPublicKey: "ssh-ed25519 host", Spec: lc.Machine{Access: []string{"desktop", "ssh"}}, Hosts: []lc.HostEntry{{Name: "client", Address: "10.77.0.11"}}, NICs: []lc.PlannedNIC{{Network: "lab", Address: "10.77.0.12", CIDR: "10.77.0.0/24", Routes: []lc.PlannedRoute{{CIDR: "10.77.1.0/24", Via: "10.77.0.20"}}}}}
	ud, err := WindowsUserData(m, []string{"10.77.255.5/32"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ud) > 16384 {
		t.Fatal("EC2 user data exceeds 16 KiB")
	}
	for _, part := range []string{"<powershell>", "OpenSSH.Server~~~~0.0.1.0", "DefaultShell", "administrators_authorized_keys", "/inheritance:r", "S-1-5-18:F", "S-1-5-32-544:F", "PasswordAuthentication no", "Rename-Computer", "exit 3010", "'renamed'", "'ready'", "Set-NetFirewallProfile -Profile Domain,Public,Private -Enabled True", "New-NetRoute", "drivers\\etc\\hosts", "dcvserver"} {
		if !strings.Contains(ud, part) {
			t.Fatal("missing bootstrap contract", part)
		}
	}
	if strings.Contains(ud, "Set-NetFirewallProfile -Enabled False") || strings.Contains(ud, "Invoke-Expression") {
		t.Fatal("unsafe bootstrap")
	}
	encoded := strings.Split(strings.Split(ud, "FromBase64String('")[1], "')")[0]
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Name, Hosts          string
		Management, Networks []string
		Desktop              bool
	}
	if err = json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != m.Name || !strings.Contains(p.Hosts, "10.77.0.11 client") || len(p.Management) != 1 || p.Management[0] != "10.77.255.5/32" || !p.Desktop || len(p.Networks) != 2 {
		t.Fatal("bootstrap payload lost network/peer inventory", p)
	}
	if _, err = WindowsUserData(m, nil); err == nil {
		t.Fatal("unrestricted management accepted")
	}
	if _, err = WindowsUserData(m, []string{"not-a-cidr"}); err == nil {
		t.Fatal("invalid management accepted")
	}
	m.HostPrivateKey = ""
	if _, err = WindowsUserData(m, []string{"10.77.255.5/32"}); err == nil {
		t.Fatal("missing host pin accepted")
	}
}

func TestWindowsUserDataPowerShellSyntax(t *testing.T) {
	ud, err := WindowsBuildUserData("ssh-ed25519 key", []string{"192.0.2.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	pscheck.Syntax(t, strings.TrimSuffix(strings.TrimPrefix(ud, "<powershell>\n"), "\n</powershell>\n<persist>true</persist>\n"))
}

func TestWindowsBuildProtectsGeneratedHostKeyBeforeStartingSSHD(t *testing.T) {
	data, err := WindowsBuildUserData("ssh-ed25519 public", []string{"192.0.2.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	generation := strings.Index(data, `ssh-keygen.exe" -A`)
	protection := strings.Index(data, `Protect-LabCentralKey (Join-Path $ssh 'ssh_host_ed25519_key')`)
	start := strings.Index(data, `Start-Service sshd`)
	if generation < 0 || protection <= generation || protection >= start {
		t.Fatal("generated host key reaches SYSTEM sshd without restricted group ownership")
	}
}
