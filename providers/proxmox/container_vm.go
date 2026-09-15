package proxmox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/OutboundSpade/LabCentral/pkg/containervm"
)

type ContainerRuntimeProfile struct {
	SchemaVersion string   `json:"schemaVersion"`
	TemplateAlias string   `json:"templateAlias"`
	Images        []string `json:"images"`
}

type canonicalContainerSpec struct {
	Image struct {
		Repository string `json:"repository"`
		Digest     string `json:"digest"`
		Ref        string `json:"ref,omitempty"`
	} `json:"image"`
	Compute struct {
		CPUMillis    uint32 `json:"cpuMillis"`
		MemoryBytes  uint64 `json:"memoryBytes"`
		Architecture string `json:"architecture"`
	} `json:"compute"`
	Isolation             string   `json:"isolation"`
	RealizationPreference []string `json:"realizationPreference,omitempty"`
	Interfaces            []struct {
		NetworkRef string `json:"networkRef"`
		IPv4       string `json:"ipv4,omitempty"`
		IPv6       string `json:"ipv6,omitempty"`
		MAC        string `json:"mac,omitempty"`
	} `json:"interfaces,omitempty"`
	Environment map[string]struct {
		Literal *string `json:"literal,omitempty"`
	} `json:"environment,omitempty"`
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// Translate only the approved closed subset, retaining the original logical ID and
// canonical input digest in the physical VM plan. Unknown fields fail decoding.
func containerVM(config TargetConfig, resource canonicalResource) (canonicalResource, containervm.Config, error) {
	var runtime containervm.Config
	if config.ContainerRuntime == nil {
		return resource, runtime, fmt.Errorf("target has no approved container VM runtime")
	}
	var spec canonicalContainerSpec
	if err := decodeEnvelope(resource.Spec, maximumConfigBytes, &spec); err != nil {
		return resource, runtime, fmt.Errorf("unsupported container fields; mounts, ports and secrets are not supported")
	}
	if spec.Isolation != "virtualMachine" || len(spec.RealizationPreference) != 1 || spec.RealizationPreference[0] != "viaVM" || len(spec.Interfaces) != 1 || spec.Compute.CPUMillis%1000 != 0 || spec.Compute.MemoryBytes < 512*1024*1024 || spec.Image.Ref != "" {
		return resource, runtime, fmt.Errorf("container requires viaVM, VM isolation, one static interface, whole CPUs and at least 512Mi memory")
	}
	image := spec.Image.Repository + "@" + spec.Image.Digest
	if !contains(config.ContainerRuntime.Images, image) {
		return resource, runtime, fmt.Errorf("container image is not preloaded in the approved runtime")
	}
	template, ok := findTemplateProfile(config, config.ContainerRuntime.TemplateAlias)
	if !ok {
		return resource, runtime, fmt.Errorf("container runtime template unavailable")
	}
	runtime = containervm.Config{SchemaVersion: containervm.Version, InstallationID: config.InstallationID, ResourceID: resource.ID, Image: image, Command: spec.Command, Args: spec.Args}
	if len(spec.Environment) > 0 {
		runtime.Environment = make(map[string]string, len(spec.Environment))
	}
	for k, v := range spec.Environment {
		if v.Literal == nil {
			return resource, runtime, fmt.Errorf("only literal environment values are supported")
		}
		runtime.Environment[k] = *v.Literal
	}
	// Validate bounded author input with placeholder binding; planning supplies real identity.
	runtime.InstanceID = "validation"
	runtime.Generation = 1
	if err := runtime.Validate(); err != nil {
		return resource, runtime, err
	}
	vm := canonicalVMSpec{OSFamily: "linux"}
	vm.Image.Repository = "labcentral/container-runtime"
	vm.Image.Digest = template.ContentDigest
	vm.Compute = spec.Compute
	vm.Interfaces = spec.Interfaces
	resource.Kind = "VM"
	resource.Spec, _ = json.Marshal(vm)
	return resource, runtime, nil
}
func runtimeBinding(config TargetConfig, plan QEMUPlan, resource canonicalResource) (canonicalResource, error) {
	if resource.Kind != "Container" {
		if plan.Container != nil {
			return resource, fmt.Errorf("unexpected container bootstrap")
		}
		return resource, nil
	}
	translated, runtime, err := containerVM(config, resource)
	if err != nil {
		return resource, err
	}
	runtime.InstanceID = plan.InstanceID
	runtime.Generation = plan.Generation
	if plan.SchemaVersion != qemuPlanV11 || !reflect.DeepEqual(plan.Container, &runtime) {
		return resource, fmt.Errorf("container bootstrap binding mismatch")
	}
	return translated, nil
}

type containerRuntimeAPI interface {
	EnsureContainerRuntime(context.Context, string, uint32, containervm.Config) error
}

func (client *Client) EnsureContainerRuntime(ctx context.Context, node string, vmid uint32, config containervm.Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	data, _ := json.Marshal(config)
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/", url.PathEscape(node), vmid)
	// Only the fixed approved guest entrypoint is executable by this adapter.
	reply, err := requestAPI[struct {
		PID int `json:"pid"`
	}](ctx, client, http.MethodPost, path+"exec", url.Values{"command": {containervm.Entrypoint, "ensure", base64.RawStdEncoding.EncodeToString(data)}})
	if err != nil {
		return err
	}
	if reply.PID <= 0 {
		return fmt.Errorf("invalid runtime process")
	}
	timeout := time.NewTimer(90 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("container bootstrap timed out")
		case <-ticker.C:
			status, e := requestAPI[struct {
				Exited       int    `json:"exited"`
				ExitCode     int    `json:"exitcode"`
				Out          string `json:"out-data"`
				Err          string `json:"err-data"`
				OutTruncated int    `json:"out-truncated"`
			}](ctx, client, http.MethodGet, path+"exec-status", url.Values{"pid": {strconv.Itoa(reply.PID)}})
			if e != nil {
				return e
			}
			if status.Exited == 0 {
				continue
			}
			if status.ExitCode != 0 || status.OutTruncated != 0 || len(status.Out) > 1024 {
				return fmt.Errorf("container bootstrap failed")
			}
			var result struct {
				SchemaVersion string `json:"schemaVersion"`
				Ready         bool   `json:"ready"`
			}
			if json.Unmarshal([]byte(status.Out), &result) != nil || result.SchemaVersion != containervm.Version || !result.Ready {
				return fmt.Errorf("container readiness response invalid")
			}
			return nil
		}
	}
}

// PVE reports inherited pool privileges on member VMs too. Never accept the
// guest-exec privilege on root or unrelated inventory objects.
func runtimeVMPath(config TargetConfig, path string) bool {
	if !strings.HasPrefix(path, "/vms/") {
		return false
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(path, "/vms/"), 10, 32)
	return err == nil && valueInRanges(uint32(id), config.VMIDRanges)
}
