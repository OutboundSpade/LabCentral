package providergrpc

import (
	"context"
	"fmt"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Access struct{ Client wire.AccessServiceClient }

func NewAccess(client wire.AccessServiceClient) *Access { return &Access{Client: client} }
func schema(s app.ProviderSchema) *wire.SchemaVersion {
	return &wire.SchemaVersion{Name: s.Name, Major: s.Major, Minor: s.Minor}
}
func Schemas(ss []*wire.SchemaVersion) []app.ProviderSchema {
	out := make([]app.ProviderSchema, 0, len(ss))
	for _, s := range ss {
		out = append(out, app.ProviderSchema{Name: s.GetName(), Major: s.GetMajor(), Minor: s.GetMinor()})
	}
	return out
}
func (a *Access) Create(ctx context.Context, r app.AccessCreate) (app.AccessCreated, error) {
	endpointDigest, err := domain.ParseSHA256Digest(r.EndpointDigest)
	if err != nil {
		return app.AccessCreated{}, fmt.Errorf("invalid access endpoint digest")
	}
	stateDigest, err := domain.ParseSHA256Digest(r.StateDigest)
	if err != nil {
		return app.AccessCreated{}, fmt.Errorf("invalid access provider-state digest")
	}
	// Domain strings include the algorithm prefix; the plugin wire contract is raw hex.
	v, err := a.Client.CreateAccess(ctx, &wire.CreateAccessRequest{Context: call(r.Call), AccessSessionId: r.SessionID, SubjectId: r.SubjectID, Method: r.Method, ExpiresAt: timestamppb.New(r.ExpiresAt), OpaqueConnectionData: r.Endpoint, EndpointSchema: schema(r.Schema), EndpointDigest: endpointDigest.Hex(), ProviderStateDigest: stateDigest.Hex(), Audience: r.Audience})
	if err != nil {
		return app.AccessCreated{}, err
	}
	if v == nil {
		return app.AccessCreated{}, fmt.Errorf("access plugin returned no result")
	}
	out := app.AccessCreated{Schema: app.ProviderSchema{Name: v.GetAccessStateSchema().GetName(), Major: v.GetAccessStateSchema().GetMajor(), Minor: v.GetAccessStateSchema().GetMinor()}, State: v.GetOpaqueAccessState(), Digest: v.GetAccessStateDigest(), Token: v.GetOneTimeToken(), URL: v.GetLaunchUrl(), Error: issue(v.GetError())}
	if v.GetValidUntil() != nil && v.GetValidUntil().IsValid() {
		out.ValidUntil = v.GetValidUntil().AsTime()
	}
	return out, nil
}
func (a *Access) Revoke(ctx context.Context, r app.AccessRevoke) (app.AccessRevoked, error) {
	v, err := a.Client.RevokeAccess(ctx, &wire.RevokeAccessRequest{Context: call(r.Call), AccessSessionId: r.SessionID, OpaqueAccessState: r.State})
	if err != nil {
		return app.AccessRevoked{}, err
	}
	if v == nil {
		return app.AccessRevoked{}, fmt.Errorf("access plugin returned no revocation result")
	}
	return app.AccessRevoked{Closed: v.GetActiveConnectionsClosed(), Error: issue(v.GetError())}, nil
}

func AccessDescriptors(values []*wire.AccessDescriptor) []domain.AccessDescriptor {
	result := make([]domain.AccessDescriptor, 0, len(values))
	for _, v := range values {
		schemas := make([]domain.AccessEndpointSchema, 0, len(v.GetEndpointSchemas()))
		for _, schema := range v.GetEndpointSchemas() {
			schemas = append(schemas, domain.AccessEndpointSchema{Name: schema.GetName(), Major: schema.GetMajor(), Minor: schema.GetMinor()})
		}
		result = append(result, domain.AccessDescriptor{EndpointSchemas: schemas, Method: v.GetMethod(), Title: v.GetTitle(), TargetKinds: append([]string(nil), v.GetTargetKinds()...), Presentation: v.GetPresentation(), MaximumOfflineSeconds: v.GetMaximumOfflineSeconds(), Renderer: v.GetRenderer()})
	}
	return result
}

func (a *Access) Download(ctx context.Context, r app.AccessDownload) (app.AccessDownloaded, error) {
	v, err := a.Client.DownloadAccess(ctx, &wire.DownloadAccessRequest{Context: call(r.Call), AccessSessionId: r.SessionID, OpaqueAccessState: r.State, DeliveryId: r.DeliveryID, OneTimeToken: r.Token})
	if err != nil {
		return app.AccessDownloaded{}, err
	}
	if v == nil || v.GetError() != nil {
		return app.AccessDownloaded{}, fmt.Errorf("access delivery failed")
	}
	return app.AccessDownloaded{Content: v.GetContent(), MediaType: v.GetMediaType(), Filename: v.GetFilename()}, nil
}
func (a *Access) Inspect(ctx context.Context, r app.AccessRevoke) (app.AccessInspection, error) {
	v, err := a.Client.InspectAccess(ctx, &wire.InspectAccessRequest{Context: call(r.Call), AccessSessionId: r.SessionID, OpaqueAccessState: r.State})
	if err != nil {
		return app.AccessInspection{}, err
	}
	if v == nil || v.GetError() != nil || v.GetEnforcedUntil() == nil || !v.GetEnforcedUntil().IsValid() {
		return app.AccessInspection{}, fmt.Errorf("invalid access enforcement status")
	}
	return app.AccessInspection{State: v.GetState(), EnforcedUntil: v.GetEnforcedUntil().AsTime(), Closed: v.GetActiveConnectionsClosed()}, nil
}
