package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"github.com/OutboundSpade/LabCentral/pkg/containervm"
	"github.com/OutboundSpade/LabCentral/pkg/ocilayout"
	"slices"
)

func (s *Service) requestContainerRuntime(ctx context.Context, parent app.ExtensionWork, build app.ExtensionWork, key string) (app.ExtensionWork, error) {
	if parent.Request.Preparation == nil || parent.Request.Preparation.Version != 2 {
		return app.ExtensionWork{}, fmt.Errorf("OCI launch requires preparation v2")
	}
	if err := s.authorizePreparation(ctx, parent.ActorID, parent.ProjectID, parent.ID); err != nil {
		return app.ExtensionWork{}, err
	}
	var output app.BuildResult
	if build.ProjectID != parent.ProjectID || build.State != domain.OperationSucceeded || build.Request.Build == nil || json.Unmarshal(build.Result, &output) != nil || output.OCI == nil || output.OCI.Validate() != nil || output.Image.MediaType != ocilayout.MediaType {
		return app.ExtensionWork{}, fmt.Errorf("verified OCI build required")
	}
	var binding BuilderBinding
	count := 0
	for _, b := range s.builders {
		if b.Kind == "container-runtime" && b.ContainerRuntime != nil && slices.Contains(b.Projects, parent.ProjectID) {
			binding = b
			count++
		}
	}
	if count != 1 {
		return app.ExtensionWork{}, fmt.Errorf("one project runtime packager binding required")
	}
	settings := containervm.RuntimeBuildSettings{Policy: *binding.ContainerRuntime, Image: ocilayout.Identity(*output.OCI)}
	config, _ := json.Marshal(settings)
	evidence := domain.ContainerRuntimeImage(settings.Evidence())
	contextBytes := []byte(`{"version":1,"files":[{"path":"runtime.json","data":"e30="}],"inputs":[]}`)
	ref, digest, err := s.commitExtensionArtifact(ctx, contextBytes)
	if err != nil {
		return app.ExtensionWork{}, err
	}
	limits, _ := json.Marshal(binding.Limits)
	identity := digestBytes("container.runtime-build/v1", config, []byte(output.Image.Digest), []byte(binding.PluginDigest), binding.Configuration, limits, []byte(binding.InstanceID), []byte(binding.ID))
	recipe := domain.BuildRecipe{ID: "runtime", Builder: binding.InstanceID + "/" + binding.ID, Schema: containervm.RuntimeBuildSchema, Architecture: "amd64", ConfigurationJSON: string(config), BaseImageDigests: []string{"sha256:" + settings.Policy.BaseSHA256}}
	request := app.ExtensionWorkRequest{Version: 2, PreparationID: parent.ID, Kind: domain.WorkBuild, PluginInstance: binding.InstanceID, PluginDigest: binding.PluginDigest, BindingID: binding.ID, ConfigurationDigest: digestBytes("extension.configuration/v1", binding.Configuration), TemplateID: parent.Request.TemplateID, DedupKey: identity.Hex(), Build: &app.BuildWork{ContainerRuntime: &evidence, Dependencies: []app.BuildDependencyWork{{RecipeID: "oci", BuildID: build.ID}}, RevisionID: parent.Request.Preparation.RevisionID, Recipe: recipe, RecipeDigest: identity, ContextArtifact: ref, ContextDigest: digest, ContextSize: uint64(len(contextBytes)), Limits: binding.Limits}}
	return s.newExtensionWork(ctx, parent.ActorID, parent.ProjectID, request, key)
}
