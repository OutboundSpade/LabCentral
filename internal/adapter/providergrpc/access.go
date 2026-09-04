package providergrpc

import (
	"context"
	"fmt"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
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
	v, err := a.Client.CreateAccess(ctx, &wire.CreateAccessRequest{Context: call(r.Call), AccessSessionId: r.SessionID, SubjectId: r.SubjectID, Method: r.Method, ExpiresAt: timestamppb.New(r.ExpiresAt), OpaqueConnectionData: r.Endpoint, EndpointSchema: schema(r.Schema), EndpointDigest: r.EndpointDigest, ProviderStateDigest: r.StateDigest, Audience: r.Audience})
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
