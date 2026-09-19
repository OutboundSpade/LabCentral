package localsecret

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileResolverBindsNonInstanceImageWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("scoped"), 0600); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewFileResolver([]string{path}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []domain.WorkSubjectKind{domain.WorkImageImport, domain.WorkPreparation, domain.WorkBuild, domain.WorkSource, domain.WorkInstance} {
		t.Run(string(kind), func(t *testing.T) {
			scope := app.InvocationScope{InvocationID: "inv_test", WorkSubject: &domain.WorkSubject{Kind: kind, ID: "op_test"}, ExpiresAt: time.Now().Add(time.Minute)}
			request := app.AuthorizedSecretRef{Reference: domain.SecretRef{Source: "file", Name: path, Key: "value"}, SubjectID: "user.test"}
			secret, err := resolver.Resolve(context.Background(), request, scope)
			allowed := kind == domain.WorkImageImport || kind == domain.WorkPreparation
			if (err == nil) != allowed {
				t.Fatalf("work scope accepted=%v, error=%v", allowed, err)
			}
			if secret != nil {
				secret.Destroy()
			}
			for _, change := range []func(*app.InvocationScope){func(s *app.InvocationScope) { s.InstanceID = "inst_test"; s.ResourceID = "vm.test" }, func(s *app.InvocationScope) { s.ExpiresAt = time.Now().Add(-time.Second) }, func(s *app.InvocationScope) { s.WorkSubject = &domain.WorkSubject{Kind: kind, ID: "../invalid"} }} {
				invalid := scope
				change(&invalid)
				if secret, err := resolver.Resolve(context.Background(), request, invalid); err == nil {
					secret.Destroy()
					t.Fatal("invalid or mixed scope accepted")
				}
			}
		})
	}
}
