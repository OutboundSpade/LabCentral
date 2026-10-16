//go:build !faultinject

package faultinject

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestDefaultBuildIgnoresFaultEnvironment(t *testing.T) {
	t.Setenv("LABCENTRAL_FAULT", `{"operation":"aws/network.create","step":"CreateVpc","fault":"kill-before"}`)
	value, err := Call(WithOperation(context.Background(), "aws", "network.create"), "CreateVpc", func(context.Context) (string, error) { return "performed", nil })
	if err != nil || value != "performed" {
		t.Fatal(value, err)
	}
	t.Setenv("LABCENTRAL_FAULT", "invalid JSON")
	if _, err := Call(context.Background(), "ssh.exec", func(context.Context) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseWorkflowDoesNotEnableFaultTag(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "faultinject") {
		t.Fatal("release workflow enables fault injection")
	}
	if !strings.Contains(string(b), "go build") {
		t.Fatal("release build guard needs updating for changed workflow")
	}
}
