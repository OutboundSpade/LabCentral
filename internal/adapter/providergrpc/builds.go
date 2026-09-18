package providergrpc

import (
	"context"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"io"
)

func artifact(a app.ExtensionArtifact) *wire.ArtifactHandle {
	return &wire.ArtifactHandle{Digest: a.Digest.Hex(), Size: a.Size, MediaType: a.MediaType, TransferToken: a.TransferToken}
}
func readArtifact(a *wire.ArtifactHandle) (app.ExtensionArtifact, error) {
	d, e := domain.ParseSHA256Digest(a.GetDigest())
	if e != nil || a.GetSize() == 0 || len(a.GetTransferToken()) < 16 || len(a.GetTransferToken()) > 256 || len(a.GetMediaType()) > 128 {
		return app.ExtensionArtifact{}, fmt.Errorf("invalid artifact")
	}
	return app.ExtensionArtifact{Digest: d, Size: a.GetSize(), MediaType: a.GetMediaType(), TransferToken: a.GetTransferToken()}, nil
}
func (s *Source) WriteArtifact(ctx context.Context, c app.ProviderCall, a app.ExtensionArtifact, r io.Reader) (app.ExtensionArtifact, error) {
	stream, e := s.Artifacts.Write(ctx)
	if e != nil {
		return app.ExtensionArtifact{}, e
	}
	buf := make([]byte, 64<<10)
	var total uint64
	for {
		n, e := r.Read(buf)
		if n > 0 {
			total += uint64(n)
			if total > a.Size {
				return app.ExtensionArtifact{}, fmt.Errorf("input exceeds bound")
			}
			if err := stream.Send(&wire.ArtifactTransferServiceWriteRequest{Context: call(c), Artifact: artifact(a), Chunk: buf[:n]}); err != nil {
				return app.ExtensionArtifact{}, err
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return app.ExtensionArtifact{}, e
		}
	}
	if total != a.Size {
		return app.ExtensionArtifact{}, fmt.Errorf("input truncated")
	}
	v, e := stream.CloseAndRecv()
	if e != nil {
		return app.ExtensionArtifact{}, e
	}
	out, e := readArtifact(v.GetArtifact())
	if e == nil && (out.Digest != a.Digest || out.Size != a.Size) {
		e = fmt.Errorf("input receipt mismatch")
	}
	return out, e
}

type Builder struct {
	Source
	Client wire.BuilderServiceClient
}

func buildRecipe(r app.BuildRequest) *wire.BuildRecipe {
	inputs := []*wire.ArtifactHandle{}
	for _, a := range r.Inputs {
		inputs = append(inputs, artifact(a))
	}
	dependencies := []*wire.BuildDependency{}
	for _, dependency := range r.Dependencies {
		dependencies = append(dependencies, &wire.BuildDependency{RecipeId: dependency.RecipeID, Artifact: artifact(dependency.Artifact)})
	}
	return &wire.BuildRecipe{Dependencies: dependencies, Digest: r.Digest.Hex(), Configuration: &wire.ExtensionConfiguration{Schema: &wire.SchemaVersion{Name: r.Recipe.Schema, Major: 1}, CanonicalJson: []byte(r.Recipe.ConfigurationJSON)}, Inputs: inputs, BaseImageDigests: r.Recipe.BaseImageDigests, Architecture: r.Recipe.Architecture}
}
func buildLimits(l app.BuildLimits) *wire.BuildLimits {
	return &wire.BuildLimits{TimeoutSeconds: l.TimeoutSeconds, CpuMillis: l.CPUCount * 1000, MemoryBytes: l.MemoryBytes, MaximumInputBytes: l.InputBytes, MaximumOutputBytes: l.OutputBytes, AllowedEgress: l.Egress}
}
func (b *Builder) ValidateBuild(ctx context.Context, r app.BuildRequest) error {
	v, e := b.Client.Validate(ctx, &wire.BuilderServiceValidateRequest{Recipe: buildRecipe(r), Limits: buildLimits(r.Limits)})
	if e == nil && len(v.GetErrors()) > 0 {
		e = fmt.Errorf("builder rejected recipe")
	}
	return e
}
func buildResult(v *wire.BuilderServiceBuildResponse) (string, app.BuildResult, error) {
	if v == nil || v.GetError() != nil {
		return "", app.BuildResult{}, fmt.Errorf("builder invocation failed")
	}
	out := app.BuildResult{}
	if v.GetState() == "succeeded" {
		var e error
		out.Image, e = readArtifact(v.GetImage())
		if e != nil {
			return "", out, e
		}
		out.ProvenanceDigest, e = domain.ParseSHA256Digest(v.GetProvenanceDigest())
		if e != nil {
			return "", out, e
		}
	}
	return v.GetState(), out, nil
}
func (b *Builder) Build(ctx context.Context, r app.BuildRequest) (string, app.BuildResult, error) {
	stream, e := b.Client.Build(ctx, &wire.BuilderServiceBuildRequest{BindingConfiguration: &wire.ExtensionConfiguration{Schema: &wire.SchemaVersion{Name: "builder.configuration", Major: 1}, CanonicalJson: r.BindingConfiguration}, Context: call(r.Call), BindingId: r.BindingID, Recipe: buildRecipe(r), Limits: buildLimits(r.Limits)})
	if e != nil {
		return "", app.BuildResult{}, e
	}
	for n := 0; n < 4096; n++ {
		v, e := stream.Recv()
		if e != nil {
			return "", app.BuildResult{}, e
		}
		if v.GetState() != "running" && v.GetState() != "queued" {
			return buildResult(v)
		}
	}
	return "", app.BuildResult{}, fmt.Errorf("builder progress exceeds bounds")
}
func (b *Builder) RecoverBuild(ctx context.Context, c app.ProviderCall) (string, app.BuildResult, error) {
	v, e := b.Client.GetInvocation(ctx, &wire.BuilderServiceGetInvocationRequest{Context: call(c)})
	if e != nil {
		return "", app.BuildResult{}, e
	}
	return buildResult(v.GetResult())
}
func (b *Builder) CancelBuild(ctx context.Context, c app.ProviderCall) error {
	v, e := b.Client.Cancel(ctx, &wire.BuilderServiceCancelRequest{Context: call(c)})
	if e == nil && v.GetOutcome() != "cancelled" && v.GetOutcome() != "absent" && v.GetOutcome() != "completed" {
		e = fmt.Errorf("builder cancellation uncertain")
	}
	return e
}
func (p *Provider) ReadArtifact(ctx context.Context, c app.ProviderCall, a app.ExtensionArtifact) (io.ReadCloser, error) {
	return (&Source{Artifacts: p.Artifacts}).ReadArtifact(ctx, c, a)
}
func (p *Provider) WriteArtifact(ctx context.Context, c app.ProviderCall, a app.ExtensionArtifact, r io.Reader) (app.ExtensionArtifact, error) {
	if p.Artifacts == nil {
		return app.ExtensionArtifact{}, fmt.Errorf("artifact transfer unavailable")
	}
	return (&Source{Artifacts: p.Artifacts}).WriteArtifact(ctx, c, a, r)
}
func imageBinding(b app.ImageBinding) *wire.TargetImageBinding {
	return &wire.TargetImageBinding{ImageRef: b.ImageRef, ProviderImageId: b.ProviderImageID, ArtifactDigest: b.ArtifactDigest.Hex(), TargetConfigurationDigest: b.TargetConfigurationDigest, Architecture: b.Architecture, OpaqueBinding: b.OpaqueBinding}
}
func importResult(v *wire.ImageImportServiceImportResponse) (string, app.ImageBinding, error) {
	out := app.ImageBinding{}
	if v == nil || v.GetError() != nil {
		return "", out, fmt.Errorf("image import failed")
	}
	if v.GetState() == "succeeded" {
		b := v.GetBinding()
		d, e := domain.ParseSHA256Digest(b.GetArtifactDigest())
		if e != nil || len(b.GetOpaqueBinding()) > 64<<10 || len(b.GetProviderImageId()) == 0 || len(b.GetProviderImageId()) > 512 {
			return "", out, fmt.Errorf("invalid image binding")
		}
		if _, e = domain.NewImageRef(b.GetImageRef()); e != nil {
			return "", out, e
		}
		out = app.ImageBinding{ImageRef: b.GetImageRef(), ProviderImageID: b.GetProviderImageId(), ArtifactDigest: d, TargetConfigurationDigest: b.GetTargetConfigurationDigest(), Architecture: b.GetArchitecture(), OpaqueBinding: b.GetOpaqueBinding()}
	}
	return v.GetState(), out, nil
}
func (p *Provider) ImportImage(ctx context.Context, r app.ImportRequest) (string, app.ImageBinding, error) {
	if p.ImageImports == nil {
		return "", app.ImageBinding{}, fmt.Errorf("image import unavailable")
	}
	v, e := p.ImageImports.Import(ctx, &wire.ImageImportServiceImportRequest{Context: call(r.Call), Target: target(r.Target), Artifact: artifact(r.Artifact), Architecture: r.Architecture, ProvenanceDigest: r.ProvenanceDigest.Hex(), ReservedPlan: importPlan(r.ReservedPlan)})
	if e != nil {
		return "", app.ImageBinding{}, e
	}
	return importResult(v)
}
func (p *Provider) RecoverImport(ctx context.Context, r app.ImportRequest) (string, app.ImageBinding, error) {
	if p.ImageImports == nil {
		return "", app.ImageBinding{}, fmt.Errorf("image import unavailable")
	}
	v, e := p.ImageImports.GetInvocation(ctx, &wire.ImageImportServiceGetInvocationRequest{Context: call(r.Call), Target: target(r.Target)})
	if e != nil {
		return "", app.ImageBinding{}, e
	}
	return importResult(v.GetResult())
}
func (p *Provider) ValidateImageBinding(ctx context.Context, r app.ImportRequest, b app.ImageBinding) error {
	if p.ImageImports == nil {
		return fmt.Errorf("image import unavailable")
	}
	v, e := p.ImageImports.ValidateBinding(ctx, &wire.ImageImportServiceValidateBindingRequest{Context: call(r.Call), Target: target(r.Target), Binding: imageBinding(b)})
	if e == nil && (!v.GetValid() || v.GetError() != nil) {
		e = fmt.Errorf("image binding no longer valid")
	}
	return e
}
