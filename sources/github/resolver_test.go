package github

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
)

func TestResolvePinsOneCommitAndChecksCompleteSource(t *testing.T) {
	root := "../../examples/template-library"
	paths := []string{"index.yaml", "composed-network/bundle.yaml", "composed-network/lab.yaml", "composed-network/deps/base/network.yaml"}
	blobs := map[string][]byte{}
	entries := []treeEntry{}
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...))
		id := hex.EncodeToString(hash[:])
		blobs[id] = data
		entries = append(entries, treeEntry{Path: "library/" + p, Mode: "100644", Type: "blob", SHA: id, Size: int64(len(data))})
	}
	commit := strings.Repeat("a", 40)
	pinned := ""
	truncated := false
	symlink := false
	commits := 0
	tampered := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("scoped credential missing")
		}
		switch {
		case r.URL.Path == "/repos/org/repo/commits/main":
			commits++
			json.NewEncoder(w).Encode(map[string]string{"sha": commit})
		case r.URL.Path == "/repos/org/repo/git/trees/"+commit:
			if pinned != commit {
				t.Error("tree fetched before durable pin")
			}
			if symlink {
				entries[1].Mode = "120000"
			} else {
				entries[1].Mode = "100644"
			}
			json.NewEncoder(w).Encode(struct {
				Truncated bool        `json:"truncated"`
				Tree      []treeEntry `json:"tree"`
			}{truncated, entries})
		case strings.HasPrefix(r.URL.Path, "/repos/org/repo/git/blobs/"):
			id := strings.TrimPrefix(r.URL.Path, "/repos/org/repo/git/blobs/")
			data := blobs[id]
			if tampered {
				data = []byte("changed")
			}
			json.NewEncoder(w).Encode(map[string]any{"sha": id, "encoding": "base64", "content": base64.StdEncoding.EncodeToString(data), "size": len(data)})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := Configuration{Owner: "org", Repository: "repo", Reference: "main", Index: "library/index.yaml"}
	resolver := newResolver(config, []byte("test-token"))
	resolver.base = server.URL
	collection, err := resolver.resolve(context.Background(), config, "", func(value string) error { pinned = value; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if collection.Commit != commit || len(collection.Entries) != 1 {
		t.Fatal("collection identity lost")
	}
	bundle, err := labbundle.Read(collection.Entries[0].Archive)
	if err != nil || bundle.Manifest.Package == nil {
		t.Fatal("invalid portable result")
	}
	again, err := resolver.resolve(context.Background(), config, pinned, func(string) error { t.Fatal("repinned known invocation"); return nil })
	if err != nil || commits != 1 || string(again.Entries[0].Archive) != string(collection.Entries[0].Archive) {
		t.Fatal("recovery changed source identity")
	}
	for _, test := range []struct {
		name  string
		setup func()
	}{{"truncated", func() { truncated = true }}, {"symlink", func() { truncated = false; symlink = true }}, {"tampered", func() { symlink = false; tampered = true }}} {
		t.Run(test.name, func(t *testing.T) {
			test.setup()
			if _, err := resolver.resolve(context.Background(), config, pinned, func(string) error { return nil }); err == nil {
				t.Fatal("accepted invalid source")
			}
		})
	}
}
func TestCollectionJournalRecoveryAndCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"owner":"org","repository":"repo","reference":"main","index":"library/index.yaml"}`)
	request := &wire.SourceServiceResolveCollectionRequest{Context: &wire.CallContext{InvocationId: "invocation.test", FencingToken: 1}, BindingId: "repo", Reference: "main", Configuration: &wire.ExtensionConfiguration{CanonicalJson: config}}
	pinned := strings.Repeat("a", 40)
	s.resolve = func(_ context.Context, _ Configuration, _ []byte, prior string, pin func(string) error) (Collection, error) {
		if prior != "" {
			t.Fatal("unexpected pin")
		}
		if err := pin(pinned); err != nil {
			t.Fatal(err)
		}
		return Collection{}, ErrRemoteUnavailable
	}
	if _, err = s.ResolveCollection(context.Background(), request); err == nil {
		t.Fatal("expected response loss")
	}
	// Restart loads the durable pin without needing an in-memory cache.
	s, err = New(root)
	if err != nil {
		t.Fatal(err)
	}
	s.resolve = func(_ context.Context, _ Configuration, _ []byte, prior string, pin func(string) error) (Collection, error) {
		if prior != pinned {
			t.Fatal("lost commit after restart")
		}
		archive, err := labbundle.ExportSource([]byte("kind: Lab\n"))
		return Collection{Commit: prior, Entries: []ResolvedEntry{{Entry: Entry{Key: "lab", Name: "lab"}, Archive: archive}}}, err
	}
	request.Context.FencingToken = 2
	recovered, err := s.GetInvocation(context.Background(), &wire.SourceServiceGetInvocationRequest{Context: request.Context})
	if err != nil || recovered.State != "resumable" {
		t.Fatal("pending commit not recoverable")
	}
	result, err := s.ResolveCollection(context.Background(), request)
	if err != nil || result.ResolvedIdentity != pinned {
		t.Fatal("recovery failed", err)
	}

	if err = os.Remove(filepath.Join(root, result.Entries[0].Bundle.Digest+".bundle")); err != nil {
		t.Fatal(err)
	}
	recovered, err = s.GetInvocation(context.Background(), &wire.SourceServiceGetInvocationRequest{Context: request.Context})
	if err != nil || recovered.State != "resumable" {
		t.Fatal("evicted source cache cannot recover")
	}
	result, err = s.ResolveCollection(context.Background(), request)
	if err != nil || result.ResolvedIdentity != pinned {
		t.Fatal("cache recovery changed commit")
	}
	request.Reference = "another"
	if _, err = s.ResolveCollection(context.Background(), request); err == nil {
		t.Fatal("unconfigured reference accepted")
	}
	request.Reference = "main"
	if _, err = s.Cancel(context.Background(), &wire.SourceServiceCancelRequest{Context: request.Context}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveCollection(context.Background(), request); err == nil {
		t.Fatal("cancelled invocation restarted")
	}
	request.Context.FencingToken = 1
	if _, err = s.GetInvocation(context.Background(), &wire.SourceServiceGetInvocationRequest{Context: request.Context}); err == nil {
		t.Fatal("stale fence accepted")
	}
}
