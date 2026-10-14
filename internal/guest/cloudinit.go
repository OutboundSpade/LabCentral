package guest

import (
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
	"strings"
)

type user struct {
	Name   string   `yaml:"name"`
	Groups string   `yaml:"groups"`
	Shell  string   `yaml:"shell"`
	Sudo   []string `yaml:"sudo"`
	Keys   []string `yaml:"ssh_authorized_keys"`
}
type hostKeys struct {
	Private string `yaml:"ed25519_private"`
	Public  string `yaml:"ed25519_public"`
}
type writeFile struct {
	Path        string `yaml:"path"`
	Content     string `yaml:"content"`
	Permissions string `yaml:"permissions"`
}
type config struct {
	Files    []writeFile `yaml:"write_files,omitempty"`
	Hostname string      `yaml:"hostname"`
	Manage   bool        `yaml:"manage_etc_hosts"`
	Users    []user      `yaml:"users"`
	SSHKeys  hostKeys    `yaml:"ssh_keys"`
	Delete   bool        `yaml:"ssh_deletekeys"`
	Password bool        `yaml:"ssh_pwauth"`
	Root     bool        `yaml:"disable_root"`
	Commands []string    `yaml:"runcmd,omitempty"`
}

func CloudInit(m lc.PlannedMachine) (string, error) {
	if m.HostPrivateKey == "" || m.HostPublicKey == "" || m.PublicKey == "" {
		return "", fmt.Errorf("instance keys are required")
	}
	c := config{Hostname: m.Name, Manage: false, Files: []writeFile{{Path: "/etc/hosts", Content: Hosts(m), Permissions: "0644"}}, Users: []user{{"lab", "sudo", "/bin/bash", []string{"ALL=(ALL) NOPASSWD:ALL"}, []string{strings.TrimSpace(m.PublicKey)}}}, SSHKeys: hostKeys{m.HostPrivateKey, strings.TrimSpace(m.HostPublicKey)}, Delete: false, Password: false, Root: true}
	if len(m.NICs) > 0 {
		netplan, err := Netplan(m, "aws")
		if err != nil {
			return "", err
		}
		c.Files = append(c.Files, writeFile{Path: "/etc/labcentral-network.yaml", Content: netplan, Permissions: "0600"}, writeFile{Path: "/usr/local/sbin/labcentral-network", Content: AWSNetworkScript, Permissions: "0700"}, writeFile{Path: "/etc/cloud/cloud.cfg.d/99-labcentral-network.cfg", Content: "network: {config: disabled}\n", Permissions: "0644"})
		c.Commands = []string{"/usr/local/sbin/labcentral-network", "rm -f /etc/netplan/50-cloud-init.yaml && netplan generate && netplan apply"}
	}
	if len(m.NICs) > 0 || m.Spec.Forwarding {
		forwarding := "0"
		if m.Spec.Forwarding {
			forwarding = "1"
		}
		c.Files = append(c.Files, writeFile{Path: "/etc/sysctl.d/90-labcentral-router.conf", Content: "net.ipv4.ip_forward=" + forwarding + "\n", Permissions: "0644"})
		c.Commands = append(c.Commands, "sysctl -p /etc/sysctl.d/90-labcentral-router.conf")
	}
	b, e := yaml.Marshal(c)
	return "#cloud-config\n" + string(b), e
}

// Hosts binds portable machine names to this instance's private addresses.
func Hosts(m lc.PlannedMachine) string {
	text := "127.0.0.1 localhost\n::1 localhost ip6-localhost\n"
	for _, h := range m.Hosts {
		text += h.Address + " " + h.Name + "\n"
	}
	return text
}
