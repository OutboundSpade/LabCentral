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
}

func CloudInit(m lc.PlannedMachine) (string, error) {
	if m.HostPrivateKey == "" || m.HostPublicKey == "" || m.PublicKey == "" {
		return "", fmt.Errorf("instance keys are required")
	}
	b, e := yaml.Marshal(config{Hostname: m.Name, Manage: false, Files: []writeFile{{Path: "/etc/hosts", Content: Hosts(m), Permissions: "0644"}}, Users: []user{{"lab", "sudo", "/bin/bash", []string{"ALL=(ALL) NOPASSWD:ALL"}, []string{strings.TrimSpace(m.PublicKey)}}}, SSHKeys: hostKeys{m.HostPrivateKey, strings.TrimSpace(m.HostPublicKey)}, Delete: false, Password: false, Root: true})
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
