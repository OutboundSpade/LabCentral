package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type MutationAPI interface {
	DiscoveryClient
	EffectivePermissions(context.Context) (EffectivePermissions, error)
	AdvisoryVMIDAvailable(context.Context, uint32) (bool, error)
	VMConfig(context.Context, string, uint32) (VMConfig, error)
	CloneVM(context.Context, CloneRequest) (UPID, error)
	ConfigureVM(context.Context, ConfigureRequest) (UPID, error)
	VMStatus(context.Context, string, uint32) (VMStatus, error)
	LifecycleVM(context.Context, LifecycleRequest) (UPID, error)
	GuestAgentPing(context.Context, string, uint32) error
	TaskStatus(context.Context, string, UPID) (TaskStatus, error)
	DeleteVM(context.Context, string, uint32) (UPID, error)
}

type EffectivePermissions struct {
	Paths            map[string][]string `json:"paths"`
	PropagatingPaths map[string][]string `json:"propagatingPaths,omitempty"`
}

type VMConfig struct {
	Digest      string            `json:"digest"`
	Description string            `json:"description,omitempty"`
	Tags        string            `json:"tags,omitempty"`
	Fields      map[string]string `json:"fields"`
}

type CloneRequest struct {
	SourceNode  string
	SourceVMID  uint32
	NewVMID     uint32
	Name        string
	TargetNode  string
	StorageID   string
	Format      string
	Pool        string
	Description string
}

type ConfigureRequest struct {
	Node        string
	VMID        uint32
	Digest      string
	Sockets     uint32
	Cores       uint32
	CPULimit    string
	MemoryMiB   uint64
	Description string
	Tags        string
	Networks    []NetworkBinding
}

type VMStatus struct {
	Status string
	VMID   uint32
	Name   string
}

type LifecycleRequest struct {
	Node   string
	VMID   uint32
	Action string
}

type TaskStatus struct {
	Status     string
	ExitStatus string
}

type UPID struct {
	Raw      string
	Node     string
	Type     string
	ObjectID string
	User     string
}

var upidHexPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8,16}$`)
var upidTypePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var taskUserPattern = regexp.MustCompile(`^[^\s/:@!]{1,128}@[A-Za-z][A-Za-z0-9._-]{0,63}(?:![A-Za-z][A-Za-z0-9._-]{0,127})?$`)
var vmNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func ParseUPID(value string) (UPID, error) {
	var zero UPID
	if len(value) == 0 || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
		return zero, fmt.Errorf("UPID is invalid")
	}
	parts := strings.Split(value, ":")
	if len(parts) != 9 || parts[0] != "UPID" || parts[8] != "" || !nodeNamePattern.MatchString(parts[1]) || !upidHexPattern.MatchString(parts[2]) || !upidHexPattern.MatchString(parts[3]) || !upidHexPattern.MatchString(parts[4]) || !upidTypePattern.MatchString(parts[5]) || parts[6] == "" || len(parts[6]) > 128 || !taskUserPattern.MatchString(parts[7]) {
		return zero, fmt.Errorf("UPID is invalid")
	}
	return UPID{Raw: value, Node: parts[1], Type: parts[5], ObjectID: parts[6], User: parts[7]}, nil
}

func (upid UPID) validate(node string, allowedTypes ...string) error {
	parsed, err := ParseUPID(upid.Raw)
	if err != nil || parsed != upid || upid.Node != node {
		return fmt.Errorf("task identity is invalid")
	}
	for _, allowed := range allowedTypes {
		if upid.Type == allowed {
			return nil
		}
	}
	return fmt.Errorf("task action is invalid")
}

func (upid UPID) validateObject(node string, vmid uint32, allowedTypes ...string) error {
	if err := upid.validate(node, allowedTypes...); err != nil || upid.ObjectID != strconv.FormatUint(uint64(vmid), 10) {
		return fmt.Errorf("task object identity is invalid")
	}
	return nil
}

func (client *Client) EffectivePermissions(ctx context.Context) (EffectivePermissions, error) {
	paths := append([]string(nil), client.permissionPaths...)
	if len(paths) == 0 {
		paths = []string{""}
	}
	result := EffectivePermissions{
		Paths:            make(map[string][]string, len(paths)),
		PropagatingPaths: make(map[string][]string, len(paths)),
	}
	for _, requestedPath := range paths {
		var query url.Values
		if requestedPath != "" {
			query = url.Values{"path": {requestedPath}}
		}
		raw, err := getAPI[map[string]map[string]apiBool](ctx, client, "/access/permissions", query)
		if err != nil {
			return EffectivePermissions{}, err
		}
		if requestedPath != "" && (len(raw) != 1 || raw[requestedPath] == nil) {
			return EffectivePermissions{}, fmt.Errorf("Proxmox permissions response did not match the requested path")
		}
		for path, privileges := range raw {
			if path == "" || !strings.HasPrefix(path, "/") || len(path) > 512 {
				return EffectivePermissions{}, fmt.Errorf("Proxmox permissions response contained an invalid path")
			}
			for privilege, propagates := range privileges {
				// PVE encodes whether a grant propagates as the value. Both 0
				// (this path only) and 1 (propagating) are effective grants at
				// the exact requested path.
				if !validField(privilege) {
					return EffectivePermissions{}, fmt.Errorf("Proxmox permissions response contained an invalid privilege")
				}
				result.Paths[path] = append(result.Paths[path], privilege)
				if bool(propagates) {
					result.PropagatingPaths[path] = append(result.PropagatingPaths[path], privilege)
				}
			}
			sortStrings(result.Paths[path])
			sortStrings(result.PropagatingPaths[path])
		}
	}
	return result, nil
}

func (client *Client) AdvisoryVMIDAvailable(ctx context.Context, vmid uint32) (bool, error) {
	value, err := getAPI[json.RawMessage](ctx, client, "/cluster/nextid", url.Values{"vmid": {strconv.FormatUint(uint64(vmid), 10)}})
	if err != nil {
		var status *APIStatusError
		if errors.As(err, &status) && status.StatusCode == 400 {
			return false, nil
		}
		return false, err
	}
	text := strings.Trim(string(value), `"`)
	parsed, parseErr := strconv.ParseUint(text, 10, 32)
	return parseErr == nil && uint32(parsed) == vmid, nil
}

func (client *Client) VMConfig(ctx context.Context, node string, vmid uint32) (VMConfig, error) {
	if !nodeNamePattern.MatchString(node) || vmid < 100 {
		return VMConfig{}, fmt.Errorf("VM config identity is invalid")
	}
	raw, err := getAPI[map[string]json.RawMessage](ctx, client, fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), nil)
	if err != nil {
		return VMConfig{}, err
	}
	result := VMConfig{Fields: make(map[string]string, len(raw))}
	for key, value := range raw {
		if !validField(key) || len(value) > maximumFieldBytes*8 {
			return VMConfig{}, fmt.Errorf("Proxmox VM config contained an invalid field")
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			var number json.Number
			if numberErr := json.Unmarshal(value, &number); numberErr != nil {
				return VMConfig{}, fmt.Errorf("Proxmox VM config contained an invalid value")
			}
			text = number.String()
		}
		if len(text) > maximumFieldBytes*4 || !validOptionalField(text) {
			return VMConfig{}, fmt.Errorf("Proxmox VM config contained an invalid value")
		}
		result.Fields[key] = text
	}
	result.Digest = result.Fields["digest"]
	result.Description = result.Fields["description"]
	result.Tags = result.Fields["tags"]
	if !sha1Pattern.MatchString(result.Digest) {
		return VMConfig{}, fmt.Errorf("Proxmox VM config omitted a valid digest")
	}
	return result, nil
}

func (client *Client) CloneVM(ctx context.Context, request CloneRequest) (UPID, error) {
	if !nodeNamePattern.MatchString(request.SourceNode) || !nodeNamePattern.MatchString(request.TargetNode) || request.SourceVMID < 100 || request.NewVMID < 100 || request.SourceVMID == request.NewVMID || !vmNamePattern.MatchString(request.Name) || !storageIDPattern.MatchString(request.StorageID) || (request.Format != "raw" && request.Format != "qcow2") || request.Pool != "" && !storageIDPattern.MatchString(request.Pool) || !strings.HasPrefix(request.Description, "LCOWN1:") || len(request.Description) > 2048 {
		return UPID{}, fmt.Errorf("clone request is invalid")
	}
	parameters := url.Values{
		"newid": {strconv.FormatUint(uint64(request.NewVMID), 10)}, "full": {"1"}, "name": {request.Name},
		"target": {request.TargetNode}, "storage": {request.StorageID}, "format": {request.Format}, "description": {request.Description},
	}
	if request.Pool != "" {
		parameters.Set("pool", request.Pool)
	}
	return client.taskRequest(ctx, "/nodes/"+request.SourceNode+"/qemu/"+strconv.FormatUint(uint64(request.SourceVMID), 10)+"/clone", parameters)
}

func (client *Client) ConfigureVM(ctx context.Context, request ConfigureRequest) (UPID, error) {
	if !nodeNamePattern.MatchString(request.Node) || request.VMID < 100 || !sha1Pattern.MatchString(request.Digest) || request.Sockets != 1 || request.Cores == 0 || request.Cores > 128 || request.CPULimit == "" || request.MemoryMiB < 16 || !strings.HasPrefix(request.Description, "LCOWN1:") || len(request.Description) > 2048 || !strings.HasPrefix(request.Tags, "labcentral_") || len(request.Networks) > 16 {
		return UPID{}, fmt.Errorf("configuration request is invalid")
	}
	parameters := url.Values{
		"digest": {request.Digest}, "sockets": {strconv.FormatUint(uint64(request.Sockets), 10)},
		"cores": {strconv.FormatUint(uint64(request.Cores), 10)}, "cpulimit": {request.CPULimit},
		"memory": {strconv.FormatUint(request.MemoryMiB, 10)}, "description": {request.Description}, "tags": {request.Tags},
		"ciupgrade": {"0"}, "onboot": {"0"},
	}
	for index, binding := range request.Networks {
		if err := validateNetworkBinding(binding); err != nil || binding.MAC == "" || binding.IPv4Address == "" {
			return UPID{}, fmt.Errorf("configuration network binding is invalid")
		}
		parameters.Set(fmt.Sprintf("net%d", index), fmt.Sprintf("virtio=%s,bridge=%s,tag=%d", strings.ToUpper(binding.MAC), binding.Bridge, binding.VLAN))
		prefix, err := netip.ParsePrefix(binding.IPv4CIDR)
		if err != nil {
			return UPID{}, err
		}
		value := fmt.Sprintf("ip=%s/%d", binding.IPv4Address, prefix.Bits())
		if binding.Gateway != "" {
			value += ",gw=" + binding.Gateway
		}
		parameters.Set(fmt.Sprintf("ipconfig%d", index), value)
	}
	return client.taskRequest(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/config", request.Node, request.VMID), parameters)
}

func (client *Client) VMStatus(ctx context.Context, node string, vmid uint32) (VMStatus, error) {
	if !nodeNamePattern.MatchString(node) || vmid < 100 {
		return VMStatus{}, fmt.Errorf("VM status identity is invalid")
	}
	raw, err := getAPI[struct {
		Status    string          `json:"status"`
		QMPStatus string          `json:"qmpstatus"`
		VMID      json.RawMessage `json:"vmid"`
		Name      string          `json:"name"`
	}](ctx, client, fmt.Sprintf("/nodes/%s/qemu/%d/status/current", node, vmid), nil)
	if err != nil {
		return VMStatus{}, err
	}
	parsedVMID, err := parseAPIUint32(raw.VMID)
	if err != nil || parsedVMID != vmid || (raw.Status != "running" && raw.Status != "stopped") || !validOptionalField(raw.Name) || !validOptionalField(raw.QMPStatus) {
		return VMStatus{}, fmt.Errorf("Proxmox VM status response was invalid")
	}
	status := raw.Status
	if status == "running" && raw.QMPStatus == "paused" {
		status = "paused"
	}
	return VMStatus{Status: status, VMID: parsedVMID, Name: raw.Name}, nil
}

func (client *Client) LifecycleVM(ctx context.Context, request LifecycleRequest) (UPID, error) {
	if !nodeNamePattern.MatchString(request.Node) || request.VMID < 100 || (request.Action != "start" && request.Action != "stop" && request.Action != "pause" && request.Action != "resume") {
		return UPID{}, fmt.Errorf("lifecycle request is invalid")
	}
	parameters := url.Values{}
	if request.Action == "pause" {
		parameters.Set("todisk", "0")
	}
	pathAction := request.Action
	if request.Action == "stop" {
		pathAction = "shutdown"
	} else if request.Action == "pause" {
		pathAction = "suspend"
	}
	return client.taskRequest(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/status/%s", request.Node, request.VMID, pathAction), parameters)
}

func (client *Client) GuestAgentPing(ctx context.Context, node string, vmid uint32) error {
	if !nodeNamePattern.MatchString(node) || vmid < 100 {
		return fmt.Errorf("guest-agent identity is invalid")
	}
	_, err := requestAPIWithNull[json.RawMessage](ctx, client, "POST", fmt.Sprintf("/nodes/%s/qemu/%d/agent/ping", node, vmid), url.Values{}, true)
	return err
}

func (client *Client) TaskStatus(ctx context.Context, node string, upid UPID) (TaskStatus, error) {
	if !nodeNamePattern.MatchString(node) {
		return TaskStatus{}, fmt.Errorf("task node is invalid")
	}
	if err := upid.validate(node, "qmclone", "qmconfig", "qmstart", "qmshutdown", "qmpause", "qmresume", "qmdestroy"); err != nil {
		return TaskStatus{}, err
	}
	raw, err := getAPI[struct {
		Status     string `json:"status"`
		ExitStatus string `json:"exitstatus"`
	}](ctx, client, "/nodes/"+node+"/tasks/"+upid.Raw+"/status", nil)
	if err != nil {
		return TaskStatus{}, err
	}
	if raw.Status != "running" && raw.Status != "stopped" {
		return TaskStatus{}, fmt.Errorf("Proxmox task status was invalid")
	}
	if raw.Status == "stopped" && raw.ExitStatus == "" {
		return TaskStatus{}, fmt.Errorf("Proxmox task exit status was missing")
	}
	if len(raw.ExitStatus) > 128 || strings.ContainsAny(raw.ExitStatus, "\r\n") {
		return TaskStatus{}, fmt.Errorf("Proxmox task exit status was invalid")
	}
	return TaskStatus{Status: raw.Status, ExitStatus: raw.ExitStatus}, nil
}

func (client *Client) DeleteVM(ctx context.Context, node string, vmid uint32) (UPID, error) {
	if !nodeNamePattern.MatchString(node) || vmid < 100 {
		return UPID{}, fmt.Errorf("delete identity is invalid")
	}
	return client.taskRequestMethod(ctx, "DELETE", fmt.Sprintf("/nodes/%s/qemu/%d", node, vmid), url.Values{"purge": {"0"}, "destroy-unreferenced-disks": {"0"}})
}

func (client *Client) taskRequest(ctx context.Context, path string, parameters url.Values) (UPID, error) {
	return client.taskRequestMethod(ctx, "POST", path, parameters)
}

func (client *Client) taskRequestMethod(ctx context.Context, method, path string, parameters url.Values) (UPID, error) {
	raw, err := requestAPI[string](ctx, client, method, path, parameters)
	if err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

func parseAPIUint32(value json.RawMessage) (uint32, error) {
	text := strings.Trim(string(value), `"`)
	parsed, err := strconv.ParseUint(text, 10, 32)
	return uint32(parsed), err
}

func sortStrings(values []string) { sort.Strings(values) }
