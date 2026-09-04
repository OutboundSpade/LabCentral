package architecture_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/OutboundSpade/LabCentral"

func TestDomainImportsOnlyStandardLibrary(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	imports := importsBelow(t, filepath.Join(root, "internal", "domain"))
	for file, paths := range imports {
		for _, path := range paths {
			if strings.Contains(path, ".") {
				t.Errorf("%s: domain imports non-standard package %q", file, path)
			}
		}
	}
}

func TestApplicationImportsOnlyDomainAndStandardLibrary(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	imports := importsBelow(t, filepath.Join(root, "internal", "application"))
	for file, paths := range imports {
		for _, path := range paths {
			if !strings.Contains(path, ".") || path == modulePath+"/internal/domain" {
				continue
			}
			t.Errorf("%s: application imports unapproved package %q", file, path)
		}
	}
}

func TestProviderAndPublicSDKBoundaries(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for file, paths := range importsBelow(t, filepath.Join(root, "providers")) {
		for _, path := range paths {
			if strings.HasPrefix(path, modulePath+"/internal/") {
				t.Errorf("%s: provider imports core internal package %q", file, path)
			}
		}
	}
	for file, paths := range importsBelow(t, filepath.Join(root, "pkg", "plugin-sdk-go")) {
		for _, path := range paths {
			if strings.HasPrefix(path, modulePath+"/internal/") {
				t.Errorf("%s: public plugin SDK imports core internal package %q", file, path)
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func importsBelow(t *testing.T, root string) map[string][]string {
	t.Helper()
	result := make(map[string][]string)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return result
		}
		t.Fatalf("stat %s: %v", root, err)
	}
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(file) != ".go" {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			result[file] = append(result[file], path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk imports below %s: %v", root, err)
	}
	return result
}

func TestOrchestrationAndPersistenceDependOnApplication(t *testing.T) {
	root := repositoryRoot(t)
	for _, area := range []string{"controlplane", "adapter/localdb"} {
		for file, paths := range importsBelow(t, filepath.Join(root, "internal", area)) {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			for _, path := range paths {
				if path == modulePath+"/internal/controlplane" || area == "controlplane" && (strings.HasPrefix(path, "google.golang.org/grpc") || strings.HasPrefix(path, "google.golang.org/protobuf") || path == modulePath+"/api/plugin/v1") {
					t.Errorf("%s imports outward dependency %s", file, path)
				}
			}
		}
	}
}
