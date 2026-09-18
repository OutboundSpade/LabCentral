package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *Service) Write(stream grpc.ClientStreamingServer[wire.ArtifactTransferServiceWriteRequest, wire.ArtifactTransferServiceWriteResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	a := first.GetArtifact()
	if !digestValid(a.GetDigest()) || a.GetSize() == 0 || a.Size > 32<<30 || len(first.Chunk) == 0 || len(first.Chunk) > channelChunkSize || (a.MediaType != QcowMedia && a.MediaType != OCIMedia && a.MediaType != "application/vnd.labcentral.build-context+json") {
		return status.Error(codes.InvalidArgument, "invalid input artifact")
	}
	if a.MediaType == "application/vnd.labcentral.build-context+json" && a.Size > 12<<20 {
		return status.Error(codes.InvalidArgument, "context too large")
	}
	s.mu.Lock()
	id, j, err := s.journal(first.Context, true)
	key := id + "/" + a.Digest
	if err == nil && (j.State != "staging" || s.writing[key]) {
		err = status.Error(codes.FailedPrecondition, "input already in use")
	}
	if err == nil {
		var total uint64
		found := false
		for _, input := range j.Inputs {
			total += input.Size
			if input.Digest == a.Digest {
				found = true
				if input.Size != a.Size || input.MediaType != a.MediaType {
					err = status.Error(codes.InvalidArgument, "input identity changed")
				}
			}
		}
		if !found {
			if len(j.Inputs) >= 9 || total+a.Size > 32<<30 {
				err = status.Error(codes.ResourceExhausted, "invocation input bound exceeded")
			} else {
				copy := proto.Clone(a).(*wire.ArtifactHandle)
				copy.TransferToken = ""
				j.Inputs = append(j.Inputs, copy)
			}
		}
		if err == nil {
			err = s.reserveStorage(id, j)
			if err == nil {
				err = s.save(id, j)
			}
		}
		if err == nil {
			s.writing[key] = true
		}
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() { s.mu.Lock(); delete(s.writing, key); s.mu.Unlock() }()
	target := filepath.Join(s.root, id, "inputs", a.Digest)
	temp := target + ".next"
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	defer os.Remove(temp)
	hash := sha256.New()
	var total uint64
	chunk := first
	for {
		if !proto.Equal(chunk.Context, first.Context) || !proto.Equal(chunk.Artifact, a) || len(chunk.Chunk) == 0 || len(chunk.Chunk) > channelChunkSize || uint64(len(chunk.Chunk)) > a.Size-total {
			return status.Error(codes.InvalidArgument, "artifact stream identity or size changed")
		}
		if _, err = io.MultiWriter(f, hash).Write(chunk.Chunk); err != nil {
			return err
		}
		total += uint64(len(chunk.Chunk))
		chunk, err = stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if total != a.Size || hex.EncodeToString(hash.Sum(nil)) != a.Digest {
		return status.Error(codes.InvalidArgument, "artifact digest or length mismatch")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, j, err = s.journal(first.Context, false)
	if err != nil {
		return err
	}
	if j.State != "staging" {
		return status.Error(codes.Canceled, "input no longer wanted")
	}
	if err = os.Rename(temp, target); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	token, err := s.ticket(id, a.Digest)
	if err != nil {
		return err
	}
	receipt := proto.Clone(a).(*wire.ArtifactHandle)
	receipt.TransferToken = token
	return stream.SendAndClose(&wire.ArtifactTransferServiceWriteResponse{Artifact: receipt})
}
func (s *Service) Read(in *wire.ArtifactTransferServiceReadRequest, out grpc.ServerStreamingServer[wire.ArtifactTransferServiceReadResponse]) error {
	s.mu.Lock()
	id, j, err := s.journal(in.GetContext(), false)
	if err == nil {
		ticket, ok := s.tickets[in.GetArtifact().GetTransferToken()]
		if !ok || ticket.Invocation != id || ticket.Digest != j.Output.Digest || time.Now().After(ticket.Expires) || j.State != "succeeded" || in.Artifact.Digest != j.Output.Digest || in.Artifact.Size != j.Output.Size || in.Artifact.MediaType != j.Output.MediaType {
			err = status.Error(codes.PermissionDenied, "invalid output capability")
		} else {
			delete(s.tickets, in.Artifact.TransferToken)
		}
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(s.root, id, "output"))
	if err != nil {
		return err
	}
	defer f.Close()
	buffer := make([]byte, channelChunkSize)
	var total uint64
	for {
		n, readErr := f.Read(buffer)
		if n > 0 {
			total += uint64(n)
			if total > j.Output.Size {
				return status.Error(codes.DataLoss, "output changed")
			}
			if err = out.Send(&wire.ArtifactTransferServiceReadResponse{Chunk: buffer[:n]}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if total != j.Output.Size {
		return status.Error(codes.DataLoss, "output truncated")
	}
	return nil
}
