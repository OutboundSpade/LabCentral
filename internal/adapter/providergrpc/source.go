package providergrpc

import (
	"context"
	"encoding/hex"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"regexp"
)

type Source struct {
	Client    wire.SourceServiceClient
	Artifacts wire.ArtifactTransferServiceClient
}

func sourceResult(r *wire.SourceServiceResolveResponse) (app.ExtensionSourceResult, error) {
	if r == nil || r.GetError() != nil {
		return app.ExtensionSourceResult{}, fmt.Errorf("source invocation failed")
	}
	p, err := domain.ParseSHA256Digest(r.GetProvenanceDigest())
	if err != nil {
		return app.ExtensionSourceResult{}, fmt.Errorf("invalid source provenance")
	}
	b := r.GetBundle()
	d, err := domain.ParseSHA256Digest(b.GetDigest())
	if err != nil || b.GetSize() == 0 || b.GetSize() > 8<<20 || len(r.GetResolvedIdentity()) == 0 || len(r.GetResolvedIdentity()) > 512 || len(b.GetTransferToken()) < 16 || len(b.GetTransferToken()) > 256 {
		return app.ExtensionSourceResult{}, fmt.Errorf("invalid source result")
	}
	return app.ExtensionSourceResult{ResolvedIdentity: r.GetResolvedIdentity(), ProvenanceDigest: p, Bundle: app.ExtensionArtifact{Digest: d, Size: b.GetSize(), MediaType: b.GetMediaType(), TransferToken: b.GetTransferToken()}}, nil
}
func (s *Source) Resolve(ctx context.Context, r app.ExtensionSourceRequest) (app.ExtensionSourceResult, error) {
	v, e := s.Client.Resolve(ctx, &wire.SourceServiceResolveRequest{Context: call(r.Call), BindingId: r.BindingID, Reference: r.Reference, Configuration: &wire.ExtensionConfiguration{Schema: &wire.SchemaVersion{Name: "source.configuration", Major: 1}, CanonicalJson: r.Configuration}, CredentialEnvelope: r.Credential})
	if e != nil {
		return app.ExtensionSourceResult{}, e
	}
	return sourceResult(v)
}
func (s *Source) Recover(ctx context.Context, c app.ProviderCall) (string, app.ExtensionSourceResult, error) {
	v, e := s.Client.GetInvocation(ctx, &wire.SourceServiceGetInvocationRequest{Context: call(c)})
	if e != nil {
		return "", app.ExtensionSourceResult{}, e
	}
	if v.GetState() != "succeeded" {
		return v.GetState(), app.ExtensionSourceResult{}, nil
	}
	if v.GetCollection() != nil {
		r, e := collectionResult(v.GetCollection())
		return v.GetState(), r, e
	}
	r, e := sourceResult(v.GetResult())
	return v.GetState(), r, e
}
func (s *Source) Cancel(ctx context.Context, c app.ProviderCall) error {
	v, e := s.Client.Cancel(ctx, &wire.SourceServiceCancelRequest{Context: call(c)})
	if e != nil {
		return e
	}
	if v.GetOutcome() != "cancelled" && v.GetOutcome() != "absent" && v.GetOutcome() != "completed" {
		return fmt.Errorf("cancellation remains uncertain")
	}
	return nil
}
func (s *Source) ReadArtifact(ctx context.Context, c app.ProviderCall, a app.ExtensionArtifact) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, e := s.Artifacts.Read(ctx, &wire.ArtifactTransferServiceReadRequest{Context: call(c), Artifact: &wire.ArtifactHandle{Digest: a.Digest.Hex(), Size: a.Size, MediaType: a.MediaType, TransferToken: a.TransferToken}})
	if e != nil {
		cancel()
		return nil, e
	}
	reader, writer := io.Pipe()
	go func() {
		defer cancel()
		var n uint64
		for {
			v, e := stream.Recv()
			if e == io.EOF {
				if n != a.Size {
					e = fmt.Errorf("artifact size mismatch")
				} else {
					e = nil
				}
				_ = writer.CloseWithError(e)
				return
			}
			if e != nil {
				_ = writer.CloseWithError(e)
				return
			}
			b := v.GetChunk()
			if len(b) == 0 || len(b) > 64<<10 || uint64(len(b)) > a.Size-n {
				_ = writer.CloseWithError(fmt.Errorf("artifact stream exceeds bounds"))
				return
			}
			n += uint64(len(b))
			if _, e = writer.Write(b); e != nil {
				return
			}
		}
	}()
	return &artifactReader{PipeReader: reader, cancel: cancel}, nil
}

type artifactReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *artifactReader) Close() error { r.cancel(); return r.PipeReader.Close() }

func collectionResult(r *wire.SourceServiceResolveCollectionResponse) (app.ExtensionSourceResult, error) {
	if r == nil || r.GetError() != nil || len(r.GetEntries()) > 32 || len(r.GetResolvedIdentity()) != 40 {
		return app.ExtensionSourceResult{}, fmt.Errorf("invalid source collection")
	}
	if _, err := hex.DecodeString(r.GetResolvedIdentity()); err != nil {
		return app.ExtensionSourceResult{}, fmt.Errorf("invalid commit")
	}
	provenance, err := domain.ParseSHA256Digest(r.GetProvenanceDigest())
	if err != nil {
		return app.ExtensionSourceResult{}, err
	}
	result := app.ExtensionSourceResult{ResolvedIdentity: r.GetResolvedIdentity(), ProvenanceDigest: provenance, Collection: &app.SourceCollection{Entries: []app.SourceCollectionEntry{}}}
	seen := map[string]bool{}
	var total uint64
	for _, entry := range r.GetEntries() {
		validated, err := sourceResult(&wire.SourceServiceResolveResponse{ResolvedIdentity: r.GetResolvedIdentity(), ProvenanceDigest: r.GetProvenanceDigest(), Bundle: entry.GetBundle()})
		if err != nil || !collectionKey.MatchString(entry.GetKey()) || seen[entry.GetKey()] || len(entry.GetName()) == 0 || len(entry.GetName()) > 63 || len(entry.GetDescription()) > 4096 {
			return app.ExtensionSourceResult{}, fmt.Errorf("invalid collection entry")
		}
		seen[entry.GetKey()] = true
		total += validated.Bundle.Size
		if total > 64<<20 {
			return app.ExtensionSourceResult{}, fmt.Errorf("collection too large")
		}
		result.Collection.Entries = append(result.Collection.Entries, app.SourceCollectionEntry{Key: entry.GetKey(), Name: entry.GetName(), Description: entry.GetDescription(), Bundle: validated.Bundle})
	}
	return result, nil
}

var collectionKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func (s *Source) ResolveCollection(ctx context.Context, r app.ExtensionSourceRequest) (app.ExtensionSourceResult, error) {
	v, e := s.Client.ResolveCollection(ctx, &wire.SourceServiceResolveCollectionRequest{Context: call(r.Call), BindingId: r.BindingID, Reference: r.Reference, Configuration: &wire.ExtensionConfiguration{Schema: &wire.SchemaVersion{Name: "source.configuration", Major: 1}, CanonicalJson: r.Configuration}, CredentialEnvelope: r.Credential})
	if e != nil {
		if status.Code(e) == codes.InvalidArgument {
			return app.ExtensionSourceResult{}, app.ErrInvalidSource
		}
		return app.ExtensionSourceResult{}, e
	}
	return collectionResult(v)
}
