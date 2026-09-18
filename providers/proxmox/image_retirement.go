package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type imageRetirementRecord struct {
	Version       string `json:"version"`
	Invocation    string `json:"invocation"`
	BindingDigest string `json:"bindingDigest"`
	Fence         uint64 `json:"fence"`
	Phase         string `json:"phase"`
	Task          string `json:"task,omitempty"`
}

func (s *ImageService) Retire(ctx context.Context, r *wire.ImageImportServiceRetireRequest) (*wire.ImageImportServiceRetireResponse, error) {
	config, client, err := s.qualified(ctx, r.GetContext(), r.GetTarget())
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	var evidence importedImageEvidence
	if err = decodeEnvelope(r.GetBinding().GetOpaqueBinding(), maximumJournalBytes, &evidence); err != nil || evidence.Version != imageImportPlanV1 || !aliasPattern.MatchString(evidence.Profile.Alias) || evidence.Profile.Alias != imageAlias(evidence.Plan) || evidence.Plan.TargetID != r.Target.TargetId || evidence.Plan.Fingerprint != r.Target.ConfigFingerprint || evidence.Ownership != imageOwnership(evidence.Plan, config) || !proto.Equal(importSuccess(evidence).Binding, r.Binding) {
		return nil, status.Error(codes.InvalidArgument, "image retirement binding mismatch")
	}
	rawConfig, _ := json.Marshal(config)
	if evidence.ConfigurationDigest != bareDigest(rawConfig) {
		return nil, status.Error(codes.FailedPrecondition, "original image configuration required")
	}
	lease, err := s.journal.AcquireClaims(ctx, r.Context.InvocationId, vmidClaimNamespace+":"+strconv.FormatUint(uint64(evidence.Plan.VMID), 10))
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	path := filepath.Join(s.root, "retire-"+bareDigest([]byte(r.Context.InvocationId))+".json")
	record := imageRetirementRecord{Version: "proxmox.image-retirement/v1", Invocation: r.Context.InvocationId, BindingDigest: bareDigest(r.Binding.OpaqueBinding), Fence: r.Context.FencingToken, Phase: "prepared"}
	raw, err := readImageFile(path)
	if err == nil {
		var stored imageRetirementRecord
		if decodeEnvelope(raw, maximumJournalBytes, &stored) != nil || stored.Version != record.Version || stored.Invocation != record.Invocation || stored.BindingDigest != record.BindingDigest || stored.Fence > record.Fence {
			return nil, status.Error(codes.FailedPrecondition, "retirement journal or fence mismatch")
		}
		record.Phase, record.Task = stored.Phase, stored.Task
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err = writeImageFile(path, record); err != nil {
		return nil, err
	}
	if record.Phase == "complete" {
		return &wire.ImageImportServiceRetireResponse{State: "retired"}, nil
	}
	profilePath := filepath.Join(s.root, evidence.Profile.Alias+".json")
	stored, err := readImageFile(profilePath)
	if err != nil {
		return nil, err
	}
	var currentEvidence importedImageEvidence
	if decodeEnvelope(stored, maximumJournalBytes, &currentEvidence) != nil {
		return nil, fmt.Errorf("invalid image retirement evidence")
	}
	currentEvidence.Retired = false
	expected, _ := json.Marshal(currentEvidence)
	if string(expected) != string(r.Binding.OpaqueBinding) {
		return nil, status.Error(codes.FailedPrecondition, "image evidence changed")
	}
	current, exists, err := readOptionalVMConfig(ctx, client, evidence.Plan.Node, evidence.Plan.VMID)
	if err != nil {
		return nil, err
	}
	if exists {
		if err = validateImportedConfig(evidence, current, config); err != nil {
			return nil, err
		}
	}
	currentEvidence.Retired = true
	if err = writeImageFile(profilePath, currentEvidence); err != nil {
		return nil, err
	}
	if exists && record.Phase == "prepared" {
		record.Phase = "delete-intent"
		if err = writeImageFile(path, record); err != nil {
			return nil, err
		}
		task, err := client.DeleteVM(ctx, evidence.Plan.Node, evidence.Plan.VMID)
		if err != nil {
			return nil, status.Error(codes.Unavailable, "image deletion outcome awaits recovery")
		}
		if task.validateObject(evidence.Plan.Node, evidence.Plan.VMID, "qmdestroy") != nil {
			return nil, fmt.Errorf("invalid image deletion task")
		}
		record.Task = task.Raw
		if err = writeImageFile(path, record); err != nil {
			return nil, err
		}
	}
	if exists && record.Phase == "delete-intent" {
		if record.Task == "" {
			return nil, status.Error(codes.Unavailable, "image deletion awaiting confirmed absence")
		}
		task, err := ParseUPID(record.Task)
		if err != nil || task.validateObject(evidence.Plan.Node, evidence.Plan.VMID, "qmdestroy") != nil {
			return nil, fmt.Errorf("invalid image deletion journal task")
		}
		if err = s.provider.waitTask(ctx, client, evidence.Plan.Node, task); err != nil {
			return nil, err
		}
	}
	_, exists, err = readOptionalVMConfig(ctx, client, evidence.Plan.Node, evidence.Plan.VMID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, status.Error(codes.Unavailable, "image deletion not yet confirmed")
	}
	record.Phase = "complete"
	if err = writeImageFile(path, record); err != nil {
		return nil, err
	}
	return &wire.ImageImportServiceRetireResponse{State: "retired"}, nil
}
