package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

func (s *ImageService) Import(ctx context.Context, r *wire.ImageImportServiceImportRequest) (*wire.ImageImportServiceImportResponse, error) {
	config, client, err := s.qualified(ctx, r.GetContext(), r.GetTarget())
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	plan, err := decodeImportPlan(r, config)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "reserved image plan mismatch")
	}
	lease, err := s.journal.AcquireClaims(ctx, plan.InvocationID, vmidClaimNamespace+":"+strconv.FormatUint(uint64(plan.VMID), 10))
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	record, err := s.load(plan.InvocationID)
	if os.IsNotExist(err) {
		f, err := os.Open(filepath.Join(s.root, imageArtifactName(plan.InvocationID)))
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, "image artifact transfer required")
		}
		stat, e := f.Stat()
		if e != nil || uint64(stat.Size()) != plan.ArtifactBytes {
			f.Close()
			return nil, status.Error(codes.FailedPrecondition, "image artifact size mismatch")
		}
		size, err := inspectStandaloneQCOW(f, config.ImageImport.MaximumDiskBytes)
		f.Close()
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid image header")
		}
		// Transfer already verified the full hash. Bind its receipt below before
		// touching the provider; recovery retains the same artifact and allocation.
		if err = s.verifyArtifactReceipt(plan, r.GetContext().GetFencingToken()); err != nil {
			return nil, err
		}
		record = imageImportRecord{Version: imageImportPlanV1, Plan: plan, Fence: r.Context.FencingToken, Phase: "prepared", DiskBytes: size}
		if err = s.save(record); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(record.Plan, plan) || record.Fence > r.Context.FencingToken {
		return nil, status.Error(codes.FailedPrecondition, "image invocation or fence changed")
	}
	record.Fence = r.Context.FencingToken
	if err = s.save(record); err != nil {
		return nil, err
	}
	return s.run(ctx, client, config, record)
}
func (s *ImageService) GetInvocation(ctx context.Context, r *wire.ImageImportServiceGetInvocationRequest) (*wire.ImageImportServiceGetInvocationResponse, error) {
	config, client, err := s.qualified(ctx, r.GetContext(), r.GetTarget())
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	// Read once only to identify the reserved VMID; re-read under its claim lock.
	record, err := s.load(r.Context.InvocationId)
	if os.IsNotExist(err) {
		return &wire.ImageImportServiceGetInvocationResponse{Result: &wire.ImageImportServiceImportResponse{State: "unknown"}}, nil
	}
	if err != nil {
		return nil, err
	}
	lease, err := s.journal.AcquireClaims(ctx, r.Context.InvocationId, vmidClaimNamespace+":"+strconv.FormatUint(uint64(record.Plan.VMID), 10))
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	record, err = s.load(r.Context.InvocationId)
	if err != nil {
		return nil, err
	}
	if record.Plan.TargetID != r.Target.TargetId || record.Plan.Fingerprint != r.Target.ConfigFingerprint || record.Fence > r.Context.FencingToken {
		return nil, status.Error(codes.FailedPrecondition, "image recovery binding changed")
	}
	record.Fence = r.Context.FencingToken
	if err = s.save(record); err != nil {
		return nil, err
	}
	result, err := s.run(ctx, client, config, record)
	return &wire.ImageImportServiceGetInvocationResponse{Result: result}, err
}
func (s *ImageService) run(ctx context.Context, client ImageImportAPI, config TargetConfig, r imageImportRecord) (*wire.ImageImportServiceImportResponse, error) {
	p := r.Plan
	filename := imageFilename(p)
	storage, _ := findStorageProfile(config, config.ImageImport.StorageAlias)
	marker := imageOwnership(p, config)
	for steps := 0; steps < 12; steps++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch r.Phase {
		case "prepared":
			available, err := client.AdvisoryVMIDAvailable(ctx, p.VMID)
			if err != nil || !available {
				return nil, status.Error(codes.FailedPrecondition, "reserved image VMID is occupied")
			}
			r.Phase = "upload-intent"
			if err = s.save(r); err != nil {
				return nil, err
			}
			f, err := os.Open(filepath.Join(s.root, imageArtifactName(p.InvocationID)))
			if err != nil {
				return nil, err
			}
			task, err := client.UploadImage(ctx, ImageUpload{Node: p.Node, Storage: config.ImageImport.StagingStorage, Filename: filename, Digest: p.ArtifactDigest, Size: p.ArtifactBytes}, f)
			f.Close()
			if err != nil {
				return nil, status.Error(codes.Unavailable, "image upload outcome awaits recovery")
			}
			r.Task = task.Raw
			if err = s.save(r); err != nil {
				return nil, err
			}
		case "upload-intent", "create-intent", "template-intent":
			kind, next := "imgcopy", "uploaded"
			if r.Phase == "create-intent" {
				kind, next = "qmcreate", "created"
			}
			if r.Phase == "template-intent" {
				kind, next = "qmtemplate", "templated"
			}
			if r.Task == "" {
				// A lost response is resolved through exact provider task evidence. Never
				// replay an uncertain create/upload or reclaim its reservation on timeout.
				task, found, err := client.RecoverImageTask(ctx, p.Node, kind, p.VMID, filename)
				if err != nil || !found {
					return nil, status.Error(codes.Unavailable, "image task outcome awaits recovery")
				}
				r.Task = task.Raw
				if err = s.save(r); err != nil {
					return nil, err
				}
			}
			task, err := ParseUPID(r.Task)
			if err != nil || task.validate(p.Node, kind) != nil || kind != "imgcopy" && task.ObjectID != strconv.FormatUint(uint64(p.VMID), 10) {
				return nil, fmt.Errorf("invalid image task identity")
			}
			if err = s.provider.waitTask(ctx, client, p.Node, task); err != nil {
				if errors.Is(err, errTaskReportedFailure) {
					r.Phase = "failed"
					if e := s.save(r); e != nil {
						return nil, e
					}
					return &wire.ImageImportServiceImportResponse{State: "failed"}, nil
				}
				return nil, status.Error(codes.Unavailable, "image task still pending")
			}
			r.Phase = next
			r.Task = ""
			if err = s.save(r); err != nil {
				return nil, err
			}
		case "uploaded":
			available, err := client.AdvisoryVMIDAvailable(ctx, p.VMID)
			if err != nil || !available {
				return nil, status.Error(codes.FailedPrecondition, "image VMID ownership conflict")
			}
			r.Phase = "create-intent"
			if err = s.save(r); err != nil {
				return nil, err
			}
			task, err := client.CreateImageVM(ctx, ImageVMCreate{CPUModel: config.ImageImport.CPUModel, Node: p.Node, VMID: p.VMID, Pool: config.Pool, Name: "lc-image-" + strconv.FormatUint(uint64(p.VMID), 10), Description: marker, Storage: storage.ID, Format: storage.Format, StagingStorage: config.ImageImport.StagingStorage, Filename: filename})
			if err != nil {
				return nil, status.Error(codes.Unavailable, "image creation outcome awaits recovery")
			}
			r.Task = task.Raw
			if err = s.save(r); err != nil {
				return nil, err
			}
		case "created":
			current, err := client.VMConfig(ctx, p.Node, p.VMID)
			if err != nil || current.Description != marker || current.Fields["lock"] != "" {
				return nil, status.Error(codes.FailedPrecondition, "imported VM ownership unavailable")
			}
			if current.Fields["template"] == "1" {
				r.Phase = "templated"
				if err = s.save(r); err != nil {
					return nil, err
				}
				continue
			}
			r.Phase = "template-intent"
			if err = s.save(r); err != nil {
				return nil, err
			}
			task, err := client.MakeImageTemplate(ctx, p.Node, p.VMID)
			if err != nil {
				return nil, status.Error(codes.Unavailable, "template conversion outcome awaits recovery")
			}
			r.Task = task.Raw
			if err = s.save(r); err != nil {
				return nil, err
			}
		case "templated":
			current, err := client.VMConfig(ctx, p.Node, p.VMID)
			if err != nil {
				return nil, err
			}
			profile := TemplateProfile{Alias: imageAlias(p), SourceNode: p.Node, VMID: p.VMID, ConfigDigest: current.Digest, ContentDigest: "sha256:" + p.ArtifactDigest, Architecture: "amd64", CloudInitDrive: true, GuestAgent: true, ForbidUnknownDevices: true, Disks: []TemplateDiskProfile{{Device: "scsi0", SizeBytes: r.DiskBytes, Boot: true}}}
			configRaw, _ := json.Marshal(config)
			evidence := importedImageEvidence{Version: imageImportPlanV1, Plan: p, ConfigurationDigest: bareDigest(configRaw), Profile: profile, Ownership: marker}
			if err = validateImportedConfig(evidence, current, config); err != nil {
				return nil, err
			}
			r.Evidence = &evidence
			r.Phase = "complete"
			if err = s.save(r); err != nil {
				return nil, err
			}
		case "complete":
			if r.Evidence != nil {
				prior, e := readImageFile(filepath.Join(s.root, r.Evidence.Profile.Alias+".json"))
				if e == nil {
					var saved importedImageEvidence
					if decodeEnvelope(prior, maximumJournalBytes, &saved) != nil || saved.Retired {
						return nil, status.Error(codes.FailedPrecondition, "imported image has been retired")
					}
				} else if !os.IsNotExist(e) {
					return nil, e
				}
			}
			if r.Evidence == nil {
				return nil, fmt.Errorf("image evidence missing")
			}
			current, err := client.VMConfig(ctx, p.Node, p.VMID)
			if err != nil {
				return nil, err
			}
			if err = validateImportedConfig(*r.Evidence, current, config); err != nil {
				return nil, err
			}
			if err = writeImageFile(filepath.Join(s.root, r.Evidence.Profile.Alias+".json"), r.Evidence); err != nil {
				return nil, err
			}
			// Remote deletion is restricted to this invocation's immutable filename.
			// Cleanup must finish before reporting success; retained evidence survives
			// restart or a lost deletion response.
			if err = client.DeleteImageStaging(ctx, p.Node, config.ImageImport.StagingStorage, filename); err != nil {
				return nil, err
			}
			if err = os.Remove(filepath.Join(s.root, imageArtifactName(p.InvocationID))); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			return importSuccess(*r.Evidence), nil
		case "failed":
			return &wire.ImageImportServiceImportResponse{State: "failed"}, nil
		default:
			return nil, fmt.Errorf("invalid image import journal phase")
		}
	}
	return nil, fmt.Errorf("image import step limit exceeded")
}
func validateImportedConfig(e importedImageEvidence, current VMConfig, config TargetConfig) error {
	raw, _ := json.Marshal(config)
	if e.Version != imageImportPlanV1 || !validRuntimeEvidence(config, e.Plan.ContainerRuntime) || e.ConfigurationDigest != bareDigest(raw) || e.Retired || e.Ownership != imageOwnership(e.Plan, config) || current.Description != e.Ownership || current.Fields["template"] != "1" || current.Fields["lock"] != "" || current.Fields["onboot"] != "0" || current.Fields["ciupgrade"] != "0" {
		return fmt.Errorf("imported image identity or ownership changed")
	}
	if config.ImageImport != nil && config.ImageImport.CPUModel != "" && current.Fields["cpu"] != config.ImageImport.CPUModel {
		return fmt.Errorf("imported image CPU model does not match qualified policy")
	}
	plan := QEMUPlan{Template: &PlanTemplate{ConfigDigest: e.Profile.ConfigDigest}, Disks: []PlanDisk{{Device: "scsi0"}}}
	if err := verifyTemplateConfig(plan, current); err != nil {
		return err
	}
	for key := range current.Fields {
		if strings.HasPrefix(key, "net") || strings.HasPrefix(key, "ipconfig") || key == "sshkeys" || key == "cipassword" || key == "cicustom" {
			return fmt.Errorf("imported template has mutable network or credentials")
		}
	}
	return nil
}
func importSuccess(e importedImageEvidence) *wire.ImageImportServiceImportResponse {
	raw, _ := json.Marshal(e)
	digest := "sha256:" + e.Plan.ArtifactDigest
	if e.Plan.ContainerRuntime != nil {
		digest = e.Plan.ContainerRuntime.ManifestDigest
	}
	return &wire.ImageImportServiceImportResponse{State: "succeeded", Binding: &wire.TargetImageBinding{ImageRef: "labcentral.local/images/" + e.Profile.Alias + "@" + digest, ProviderImageId: strconv.FormatUint(uint64(e.Plan.VMID), 10), ArtifactDigest: e.Plan.ArtifactDigest, TargetConfigurationDigest: e.Plan.Fingerprint, Architecture: "amd64", OpaqueBinding: raw}}
}
func (s *ImageService) ValidateBinding(ctx context.Context, r *wire.ImageImportServiceValidateBindingRequest) (*wire.ImageImportServiceValidateBindingResponse, error) {
	config, client, err := s.qualified(ctx, r.GetContext(), r.GetTarget())
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	var evidence importedImageEvidence
	if err = decodeEnvelope(r.GetBinding().GetOpaqueBinding(), maximumJournalBytes, &evidence); err != nil {
		return &wire.ImageImportServiceValidateBindingResponse{Valid: false}, nil
	}
	if !aliasPattern.MatchString(evidence.Profile.Alias) || evidence.Profile.Alias != imageAlias(evidence.Plan) || evidence.Plan.TargetID != r.Target.TargetId || evidence.Plan.Fingerprint != r.Target.ConfigFingerprint {
		return &wire.ImageImportServiceValidateBindingResponse{Valid: false}, nil
	}
	expected := importSuccess(evidence).Binding
	b := r.Binding
	if b.ImageRef != expected.ImageRef || b.ProviderImageId != expected.ProviderImageId || b.ArtifactDigest != expected.ArtifactDigest || b.Architecture != expected.Architecture || b.TargetConfigurationDigest != expected.TargetConfigurationDigest {
		return &wire.ImageImportServiceValidateBindingResponse{Valid: false}, nil
	}
	stored, err := readImageFile(filepath.Join(s.root, evidence.Profile.Alias+".json"))
	if err != nil || string(stored) != string(b.OpaqueBinding) {
		return &wire.ImageImportServiceValidateBindingResponse{Valid: false}, nil
	}
	current, err := client.VMConfig(ctx, evidence.Plan.Node, evidence.Plan.VMID)
	if err != nil {
		return nil, err
	}
	return &wire.ImageImportServiceValidateBindingResponse{Valid: validateImportedConfig(evidence, current, config) == nil}, nil
}
