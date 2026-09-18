package proxmox

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"io"
	"os"
	"path/filepath"
	"time"
)

func imageArtifactName(id string) string { return "artifact-" + bareDigest([]byte(id)) + ".qcow2" }
func (s *ImageService) Write(stream grpc.ClientStreamingServer[wire.ArtifactTransferServiceWriteRequest, wire.ArtifactTransferServiceWriteResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	call, a := first.GetContext(), first.GetArtifact()
	if call == nil || !invocationIDPattern.MatchString(call.InvocationId) || call.FencingToken == 0 || a.GetMediaType() != qcowArtifactMedia || a.GetSize() < 112 || !sha256Pattern.MatchString("sha256:"+a.GetDigest()) {
		return status.Error(codes.InvalidArgument, "invalid image transfer")
	}
	s.mu.Lock()
	grant, ok := s.grants[call.InvocationId]
	s.mu.Unlock()
	if !ok || time.Now().After(grant.Expires) || grant.TargetID != call.ProviderTargetId || grant.Fence != call.FencingToken || a.Size > grant.MaximumBytes {
		return status.Error(codes.PermissionDenied, "qualified image transfer grant required")
	}
	lease, err := s.journal.AcquireClaims(stream.Context(), call.InvocationId, "image-staging")
	if err != nil {
		return err
	}
	defer lease.Close()
	if record, err := s.load(call.InvocationId); err == nil {
		if record.Fence > call.FencingToken || record.Phase != "prepared" || record.Plan.ArtifactDigest != a.Digest || record.Plan.Fingerprint != grant.Fingerprint {
			return status.Error(codes.FailedPrecondition, "image transfer already bound")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	if len(entries) > 4096 {
		return status.Error(codes.ResourceExhausted, "image journal bound exceeded")
	}
	var total uint64
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".qcow2" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += uint64(info.Size())
	}
	if total+a.Size > 64<<30 {
		return status.Error(codes.ResourceExhausted, "image staging capacity exceeded")
	}
	f, err := os.CreateTemp(s.root, ".image-upload-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	var written uint64
	for message := first; ; {
		if !proto.Equal(message.Context, call) || !proto.Equal(message.Artifact, a) || len(message.Chunk) == 0 || len(message.Chunk) > 64<<10 || written+uint64(len(message.Chunk)) > a.Size {
			return status.Error(codes.InvalidArgument, "image stream binding or size changed")
		}
		if _, err = io.MultiWriter(f, h).Write(message.Chunk); err != nil {
			return err
		}
		written += uint64(len(message.Chunk))
		message, err = stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if written != a.Size || fmt.Sprintf("%x", h.Sum(nil)) != a.Digest {
		return status.Error(codes.InvalidArgument, "image checksum mismatch")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err = inspectStandaloneQCOW(f, grant.MaximumDiskBytes); err != nil {
		return status.Error(codes.InvalidArgument, "standalone bounded qcow2 required")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.root, imageArtifactName(call.InvocationId))); err != nil {
		return err
	}
	dir, err := os.Open(s.root)
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	receipt := imageArtifactReceipt{Invocation: call.InvocationId, Target: grant.TargetID, Fingerprint: grant.Fingerprint, Digest: a.Digest, Size: a.Size, Fence: call.FencingToken}
	if err = writeImageFile(filepath.Join(s.root, "receipt-"+bareDigest([]byte(call.InvocationId))+".json"), receipt); err != nil {
		return err
	}
	result := proto.Clone(a).(*wire.ArtifactHandle)
	// The transfer capability is invocation-bound and only usable by this core
	// connection; imports also require the exact durable plan and fresh target.
	result.TransferToken = bareDigest([]byte(call.InvocationId + "\x00" + a.Digest))
	return stream.SendAndClose(&wire.ArtifactTransferServiceWriteResponse{Artifact: result})
}

type imageArtifactReceipt struct {
	Invocation, Target, Fingerprint, Digest string
	Size, Fence                             uint64
}

func (s *ImageService) verifyArtifactReceipt(p importPlanDocument, fence uint64) error {
	raw, err := readImageFile(filepath.Join(s.root, "receipt-"+bareDigest([]byte(p.InvocationID))+".json"))
	if err != nil {
		return err
	}
	var r imageArtifactReceipt
	if err = json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.Invocation != p.InvocationID || r.Target != p.TargetID || r.Fingerprint != p.Fingerprint || r.Digest != p.ArtifactDigest || r.Size != p.ArtifactBytes || r.Fence > fence {
		return status.Error(codes.FailedPrecondition, "image transfer receipt mismatch")
	}
	return nil
}
