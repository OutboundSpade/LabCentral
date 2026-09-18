package vm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/sourcepolicy"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
)

func TestBuildKitPolicyRejectsUndeclaredInheritedSources(t *testing.T) {
	digest := strings.Repeat("a", 64)
	encoded, err := json.Marshal(buildSourcePolicy([]string{digest}))
	if err != nil {
		t.Fatal(err)
	}
	var policy spb.Policy
	if err = json.Unmarshal(encoded, &policy); err != nil {
		t.Fatal("buildctl cannot decode source policy", err)
	}
	engine := sourcepolicy.NewEngine([]*spb.Policy{&policy})
	for _, identifier := range []string{"docker-image://docker.io/library/busybox@sha256:" + digest, "local://context", "local://dockerfile", "oci-layout://base@sha256:" + strings.Repeat("b", 64)} {
		if _, err := engine.Evaluate(context.Background(), &pb.SourceOp{Identifier: identifier}); err != nil {
			t.Fatalf("declared source rejected: %s: %v", identifier, err)
		}
	}
	for _, identifier := range []string{"docker-image://docker.io/library/busybox:latest", "docker-image://docker.io/library/busybox@sha256:" + strings.Repeat("b", 64), "https://example.com/unpinned.tar", "git://example.com/repo"} {
		if _, err := engine.Evaluate(context.Background(), &pb.SourceOp{Identifier: identifier}); err == nil {
			t.Fatalf("undeclared source accepted: %s", identifier)
		}
	}
	if err := ValidateDockerfile([]byte("FROM docker.io/library/busybox@sha256:"+digest+"\n"), []string{digest}, nil); err != nil {
		t.Fatal("portable hexadecimal digest was not recognized", err)
	}
}
