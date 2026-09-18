package providergrpc

import (
	"context"
	"crypto/sha256"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func importPlan(p *app.ImageImportPlan) *wire.ImageImportPlan {
	if p == nil {
		return nil
	}
	out := &wire.ImageImportPlan{Opaque: p.Opaque, Digest: p.Digest.Hex()}
	for _, c := range p.Claims {
		out.Claims = append(out.Claims, &wire.AllocationClaim{Namespace: c.Namespace, Value: c.Value})
	}
	return out
}
func (p *Provider) PlanImageImport(ctx context.Context, r app.ImportRequest, excluded []app.AllocationKey) (app.ImageImportPlan, error) {
	if p.ImageImports == nil {
		return app.ImageImportPlan{}, fmt.Errorf("image import unavailable")
	}
	req := &wire.ImageImportServicePlanRequest{Input: &wire.ImageImportServiceImportRequest{Context: call(r.Call), Target: target(r.Target), Artifact: artifact(r.Artifact), Architecture: r.Architecture, ProvenanceDigest: r.ProvenanceDigest.Hex(), ConfigurationJson: importConfiguration(r)}}
	for _, c := range excluded {
		if string(c.TargetID) != r.Target.ID {
			return app.ImageImportPlan{}, fmt.Errorf("allocation target mismatch")
		}
		req.ExcludedAllocations = append(req.ExcludedAllocations, &wire.AllocationClaim{Namespace: c.Namespace, Value: c.Value})
	}
	v, err := p.ImageImports.Plan(ctx, req)
	if err != nil {
		return app.ImageImportPlan{}, err
	}
	plan := v.GetPlan()
	d, err := domain.ParseSHA256Digest(plan.GetDigest())
	if err != nil || len(plan.GetOpaque()) == 0 || len(plan.GetOpaque()) > 1<<20 || len(plan.GetClaims()) == 0 || len(plan.GetClaims()) > 16 {
		return app.ImageImportPlan{}, fmt.Errorf("invalid import plan")
	}
	sum := sha256.Sum256(plan.GetOpaque())
	if fmt.Sprintf("%x", sum) != d.Hex() {
		return app.ImageImportPlan{}, fmt.Errorf("import plan digest mismatch")
	}
	out := app.ImageImportPlan{Opaque: plan.GetOpaque(), Digest: d}
	for _, c := range plan.GetClaims() {
		out.Claims = append(out.Claims, app.AllocationKey{TargetID: domain.ProviderTargetID(r.Target.ID), Namespace: c.GetNamespace(), Value: c.GetValue()})
	}
	return out, nil
}

func (p *Provider) RetireImage(ctx context.Context, r app.ImportRequest, b app.ImageBinding) (string, error) {
	if p.ImageImports == nil {
		return "", fmt.Errorf("image retirement unavailable")
	}
	result, err := p.ImageImports.Retire(ctx, &wire.ImageImportServiceRetireRequest{Context: call(r.Call), Target: target(r.Target), Binding: imageBinding(b)})
	if err != nil {
		return "", err
	}
	if result.GetState() != "retired" && result.GetState() != "running" {
		return "", fmt.Errorf("invalid image retirement result")
	}
	return result.State, nil
}
