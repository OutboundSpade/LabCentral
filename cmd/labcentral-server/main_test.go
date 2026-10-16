package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OutboundSpade/LabCentral/v2/hosted"
)

func TestCheckConfigReportsGroupsWithoutStartingIdentityOrProviders(t *testing.T) {
	root := t.TempDir()
	environment := filepath.Join(root, "environment.yaml")
	if err := os.WriteFile(environment, []byte(`version: 2
name: example
platform: aws
operatorCIDRs: [192.0.2.1/32]
bases:
  ubuntu-24.04: {id: ami-example, identity: ami-example, user: ubuntu, diskGiB: 8}
aws:
  account: "123456789012"
  region: us-east-1
  zone: us-east-1a
  types: [{name: t3.medium, cpu: 2, memoryMiB: 4096}]
`), 0600); err != nil {
		t.Fatal(err)
	}
	c := hosted.Config{Capacity: hosted.SiteCapacity{VCPU: 12, MemoryMiB: 24576}, MemberGroup: "example_members", AdminGroup: "example_admins", Origin: "https://labs.invalid", DesktopOrigin: "https://desktop.invalid", Issuer: "https://issuer.invalid", Environment: environment, ClientSecret: "synthetic-secret-must-not-print", Root: filepath.Join(root, "must-not-create")}
	config := filepath.Join(root, "config.json")
	for _, test := range []struct{ name, member, admin, failure string }{
		{"valid", c.MemberGroup, c.AdminGroup, ""},
		{"missing-member", "", c.AdminGroup, "memberGroup"},
		{"missing-admin", c.MemberGroup, "", "adminGroup"},
		{"whitespace", "two groups", c.AdminGroup, "memberGroup"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c.MemberGroup, c.AdminGroup = test.member, test.admin
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(config, raw, 0600); err != nil {
				t.Fatal(err)
			}
			oldFlags, oldArgs, oldOut := flag.CommandLine, os.Args, os.Stdout
			defer func() { flag.CommandLine, os.Args, os.Stdout = oldFlags, oldArgs, oldOut }()
			flag.CommandLine = flag.NewFlagSet("check-config-test", flag.ContinueOnError)
			os.Args = []string{"labcentral-server", "--config", config, "--check-config", "--provider", filepath.Join(root, "missing-provider")}
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			os.Stdout = write
			err = run()
			if closeErr := write.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			out, readErr := io.ReadAll(read)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if test.failure != "" {
				if err == nil || !strings.Contains(err.Error(), test.failure) || len(out) != 0 {
					t.Fatal(err, string(out))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				MemberGroup, AdminGroup, Platform string
				Capacity                          hosted.SiteCapacity
			}
			if err = json.Unmarshal(out, &report); err != nil || report.MemberGroup != c.MemberGroup || report.AdminGroup != c.AdminGroup || report.Platform != "aws" || report.Capacity != c.Capacity {
				t.Fatal(report, err)
			}
			if strings.Contains(string(out), c.ClientSecret) {
				t.Fatal("check-config disclosed client secret")
			}
			if _, err = os.Stat(c.Root); !os.IsNotExist(err) {
				t.Fatal("check-config opened service state", err)
			}
		})
	}
}
