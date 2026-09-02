package localsecret

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func TestFileResolverReturnsInvocationScopedEphemeralCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxmox.json")
	if err := os.WriteFile(path, []byte(`{"tokenID":"labcentral@pve!target","tokenSecret":"canary"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewFileResolver([]string{path}, 0)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := resolver.Resolve(context.Background(), application.AuthorizedSecretRef{
		Reference: domain.SecretRef{Source: "file", Name: path, Key: "value"}, SubjectID: "local-admin",
	}, application.InvocationScope{InvocationID: "inv_test", InstanceID: "inst_test", ResourceID: "vm.test", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	value := secret.Bytes()
	if !strings.Contains(string(value), "canary") {
		t.Fatal("resolved secret did not contain the file value")
	}
	secret.Destroy()
	if strings.Contains(fmt.Sprintf("%v %#v", secret, secret), "canary") {
		t.Fatal("ephemeral secret formatting leaked its value")
	}
}

func TestFileResolverRejectsUnprotectedUnlistedAndSymlinkFiles(t *testing.T) {
	directory := t.TempDir()
	protected := filepath.Join(directory, "protected")
	unprotected := filepath.Join(directory, "unprotected")
	symlink := filepath.Join(directory, "symlink")
	if err := os.WriteFile(protected, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unprotected, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, symlink); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewFileResolver([]string{protected, unprotected, symlink}, 0)
	if err != nil {
		t.Fatal(err)
	}
	scope := application.InvocationScope{InvocationID: "inv_test", InstanceID: "inst_test", ResourceID: "vm.test", ExpiresAt: time.Now().Add(time.Minute)}
	for name, path := range map[string]string{"unprotected": unprotected, "symlink": symlink, "unlisted": filepath.Join(directory, "missing")} {
		t.Run(name, func(t *testing.T) {
			_, err := resolver.Resolve(context.Background(), application.AuthorizedSecretRef{Reference: domain.SecretRef{Source: "file", Name: path, Key: "value"}, SubjectID: "local-admin"}, scope)
			if err == nil {
				t.Fatal("unsafe secret file was accepted")
			}
		})
	}
}
