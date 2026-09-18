package render

import (
	"context"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/labbundle"
	"go.yaml.in/yaml/v4"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPreparationSelectsUsedPortableRecipes(t *testing.T) {
	renderer := testRenderer(t, DefaultLimits())
	for _, test := range []struct {
		name string
		want []string
	}{{"ansible-linux", []string{"workstation"}}, {"dockerfile-web", []string{"web"}}} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("..", "..", "examples", "template-library", test.name)
			raw, err := os.ReadFile(filepath.Join(root, "bundle.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest labbundle.Manifest
			if err = yaml.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{}
			for _, file := range manifest.Files {
				files[file.Path], err = os.ReadFile(filepath.Join(root, file.Path))
				if err != nil {
					t.Fatal(err)
				}
			}
			archive, err := labbundle.Pack(manifest, files)
			if err != nil {
				t.Fatal(err)
			}
			names, err := renderer.RequiredBuildRecipes(context.Background(), PackageRendererVersion, domain.RevisionContent{VerifiedBundle: archive}, nil)
			if err != nil || !reflect.DeepEqual(names, test.want) {
				t.Fatalf("selected roots %v, want %v: %v", names, test.want, err)
			}
		})
	}
}
