package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultDockerSocket = "/var/run/docker.sock"
	maxEngineResponse   = 2 << 20
)

type TargetInfo struct {
	APIVersion      string
	EngineVersion   string
	OperatingSystem string
	OSType          string
	Architecture    string
}

type NetworkCreate struct {
	Name     string
	Internal bool
	IPv4CIDR string
	IPv6CIDR string
	MTU      uint16
	Labels   map[string]string
}

type VolumeCreate struct {
	Name   string
	Labels map[string]string
}

type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type PublishedPort struct {
	ContainerPort uint16
	Protocol      string
	HostIP        string
	HostPort      uint16
}

type ContainerCreate struct {
	Name        string
	Image       string
	Entrypoint  []string
	Command     []string
	Environment []string
	Network     string
	Mounts      []Mount
	Published   []PublishedPort
	CPUMillis   uint32
	MemoryBytes uint64
	Labels      map[string]string
}

type ContainerObservation struct {
	ID      string
	Running bool
	Ports   map[string]uint16
	Labels  map[string]string
}

// Engine is the provider-local boundary around the Docker Engine REST API.
// It deliberately exposes only the primitives used by the thin local slice.
type Engine interface {
	Discover(context.Context) (TargetInfo, error)
	EnsureImage(context.Context, string) error
	EnsureNetwork(context.Context, NetworkCreate) (string, error)
	RemoveNetwork(context.Context, string, map[string]string) error
	EnsureVolume(context.Context, VolumeCreate) (string, error)
	RemoveVolume(context.Context, string, map[string]string) error
	EnsureContainer(context.Context, ContainerCreate) (ContainerObservation, error)
	InspectContainer(context.Context, string) (ContainerObservation, error)
	RemoveContainer(context.Context, string, map[string]string) error
}

func (c *EngineClient) EnsureImage(ctx context.Context, reference string) error {
	if reference == "" || !strings.Contains(reference, "@sha256:") {
		return fmt.Errorf("Docker image reference must be pinned by SHA-256")
	}
	if err := c.call(ctx, http.MethodGet, "/images/"+url.PathEscape(reference)+"/json", nil, http.StatusOK, nil, true); err == nil {
		return nil
	} else if !errors.Is(err, errNotFound) {
		return err
	}
	prefix := c.versionPrefix()
	if prefix == "" {
		if _, err := c.Discover(ctx); err != nil {
			return err
		}
		prefix = c.versionPrefix()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+prefix+"/images/create?fromImage="+url.QueryEscape(reference), nil)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("pull Docker image: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("Docker image pull returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<20))
	for {
		var progress struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&progress); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("decode Docker image pull progress: %w", err)
		}
		if progress.Error != "" || progress.ErrorDetail.Message != "" {
			message := progress.ErrorDetail.Message
			if message == "" {
				message = progress.Error
			}
			return fmt.Errorf("Docker image pull failed: %s", message)
		}
	}
	return c.call(ctx, http.MethodGet, "/images/"+url.PathEscape(reference)+"/json", nil, http.StatusOK, nil, true)
}

type EngineClient struct {
	httpClient *http.Client
	baseURL    string
	apiPrefix  atomic.Pointer[string]
}

func NewEngineClientFromEnvironment() (*EngineClient, error) {
	host := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if host == "" {
		host = "unix://" + defaultDockerSocket
	}
	return NewEngineClient(host)
}

func NewEngineClient(endpoint string) (*EngineClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Docker endpoint: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	baseURL := endpoint
	switch parsed.Scheme {
	case "unix":
		if parsed.Path == "" || !path.IsAbs(parsed.Path) {
			return nil, fmt.Errorf("Docker Unix endpoint must be an absolute socket path")
		}
		socketPath := parsed.Path
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socketPath)
		}
		baseURL = "http://docker"
	case "http":
		if parsed.Hostname() == "" {
			return nil, fmt.Errorf("Docker HTTP endpoint is missing a host")
		}
		address := net.ParseIP(parsed.Hostname())
		if parsed.Hostname() != "localhost" && (address == nil || !address.IsLoopback()) {
			return nil, fmt.Errorf("unencrypted Docker HTTP endpoint must be loopback")
		}
	case "https":
		return nil, fmt.Errorf("Docker TLS endpoint configuration is not implemented by the laptop slice")
	case "tcp":
		address := net.ParseIP(parsed.Hostname())
		if parsed.Hostname() != "localhost" && (address == nil || !address.IsLoopback()) {
			return nil, fmt.Errorf("unencrypted Docker TCP endpoint must be loopback")
		}
		parsed.Scheme = "http"
		baseURL = parsed.String()
	default:
		return nil, fmt.Errorf("unsupported Docker endpoint scheme %q", parsed.Scheme)
	}
	return &EngineClient{
		httpClient: &http.Client{Transport: transport, Timeout: 30 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}, nil
}

func (c *EngineClient) Discover(ctx context.Context) (TargetInfo, error) {
	var version struct {
		APIVersion string `json:"ApiVersion"`
		Version    string `json:"Version"`
		OS         string `json:"Os"`
		Arch       string `json:"Arch"`
	}
	if err := c.call(ctx, http.MethodGet, "/version", nil, http.StatusOK, &version, false); err != nil {
		return TargetInfo{}, err
	}
	if !supportedAPIVersion(version.APIVersion) {
		return TargetInfo{}, fmt.Errorf("Docker API %q is older than required 1.44", version.APIVersion)
	}
	prefix := "/v" + version.APIVersion
	c.apiPrefix.Store(&prefix)
	var info struct {
		OperatingSystem string `json:"OperatingSystem"`
		OSType          string `json:"OSType"`
	}
	if err := c.call(ctx, http.MethodGet, "/info", nil, http.StatusOK, &info, true); err != nil {
		return TargetInfo{}, err
	}
	return TargetInfo{
		APIVersion:      version.APIVersion,
		EngineVersion:   version.Version,
		OperatingSystem: info.OperatingSystem,
		OSType:          info.OSType,
		Architecture:    version.Arch,
	}, nil
}

func supportedAPIVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	return majorErr == nil && minorErr == nil && (major > 1 || (major == 1 && minor >= 44))
}

func (c *EngineClient) EnsureNetwork(ctx context.Context, desired NetworkCreate) (string, error) {
	if existing, err := c.inspectNetwork(ctx, desired.Name); err == nil {
		if err := verifyLabels(existing.Labels, desired.Labels); err != nil {
			return "", err
		}
		return existing.ID, nil
	} else if !errors.Is(err, errNotFound) {
		return "", err
	}
	ipamConfig := make([]map[string]string, 0, 2)
	if desired.IPv4CIDR != "" {
		ipamConfig = append(ipamConfig, map[string]string{"Subnet": desired.IPv4CIDR})
	}
	if desired.IPv6CIDR != "" {
		ipamConfig = append(ipamConfig, map[string]string{"Subnet": desired.IPv6CIDR})
	}
	options := map[string]string{}
	if desired.MTU != 0 {
		options["com.docker.network.driver.mtu"] = strconv.Itoa(int(desired.MTU))
	}
	body := map[string]any{
		"Name":       desired.Name,
		"Driver":     "bridge",
		"Internal":   desired.Internal,
		"Attachable": false,
		"Labels":     desired.Labels,
		"Options":    options,
	}
	if len(ipamConfig) > 0 {
		body["IPAM"] = map[string]any{"Driver": "default", "Config": ipamConfig}
	}
	var response struct {
		ID string `json:"Id"`
	}
	if err := c.call(ctx, http.MethodPost, "/networks/create", body, http.StatusCreated, &response, true); err != nil {
		return "", err
	}
	if response.ID == "" {
		return "", fmt.Errorf("Docker created a network without an ID")
	}
	return response.ID, nil
}

func (c *EngineClient) RemoveNetwork(ctx context.Context, id string, ownership map[string]string) error {
	existing, err := c.inspectNetwork(ctx, id)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := verifyLabels(existing.Labels, ownership); err != nil {
		return err
	}
	return c.call(ctx, http.MethodDelete, "/networks/"+url.PathEscape(existing.ID), nil, http.StatusNoContent, nil, true)
}

type networkInspect struct {
	ID     string            `json:"Id"`
	Labels map[string]string `json:"Labels"`
}

func (c *EngineClient) inspectNetwork(ctx context.Context, id string) (networkInspect, error) {
	var result networkInspect
	err := c.call(ctx, http.MethodGet, "/networks/"+url.PathEscape(id), nil, http.StatusOK, &result, true)
	return result, err
}

func (c *EngineClient) EnsureVolume(ctx context.Context, desired VolumeCreate) (string, error) {
	if existing, err := c.inspectVolume(ctx, desired.Name); err == nil {
		if err := verifyLabels(existing.Labels, desired.Labels); err != nil {
			return "", err
		}
		return existing.Name, nil
	} else if !errors.Is(err, errNotFound) {
		return "", err
	}
	var response struct {
		Name string `json:"Name"`
	}
	body := map[string]any{"Name": desired.Name, "Driver": "local", "Labels": desired.Labels}
	if err := c.call(ctx, http.MethodPost, "/volumes/create", body, http.StatusCreated, &response, true); err != nil {
		return "", err
	}
	if response.Name == "" {
		return "", fmt.Errorf("Docker created a volume without a name")
	}
	return response.Name, nil
}

func (c *EngineClient) RemoveVolume(ctx context.Context, name string, ownership map[string]string) error {
	existing, err := c.inspectVolume(ctx, name)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := verifyLabels(existing.Labels, ownership); err != nil {
		return err
	}
	return c.call(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(existing.Name), nil, http.StatusNoContent, nil, true)
}

type volumeInspect struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

func (c *EngineClient) inspectVolume(ctx context.Context, name string) (volumeInspect, error) {
	var result volumeInspect
	err := c.call(ctx, http.MethodGet, "/volumes/"+url.PathEscape(name), nil, http.StatusOK, &result, true)
	return result, err
}

func (c *EngineClient) EnsureContainer(ctx context.Context, desired ContainerCreate) (ContainerObservation, error) {
	if existing, err := c.inspectContainer(ctx, desired.Name); err == nil {
		if err := verifyLabels(existing.Labels, desired.Labels); err != nil {
			return ContainerObservation{}, err
		}
		if !existing.Running {
			if err := c.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(existing.ID)+"/start", nil, http.StatusNoContent, nil, true); err != nil {
				return ContainerObservation{}, err
			}
			return c.waitForContainer(ctx, existing.ID, desired.Published)
		}
		if len(desired.Published) > 0 {
			return c.waitForContainer(ctx, existing.ID, desired.Published)
		}
		return existing, nil
	} else if !errors.Is(err, errNotFound) {
		return ContainerObservation{}, err
	}
	exposed := map[string]map[string]string{}
	bindings := map[string][]map[string]string{}
	for _, published := range desired.Published {
		portKey := fmt.Sprintf("%d/%s", published.ContainerPort, published.Protocol)
		exposed[portKey] = map[string]string{}
		hostPort := ""
		if published.HostPort != 0 {
			hostPort = strconv.Itoa(int(published.HostPort))
		}
		bindings[portKey] = []map[string]string{{"HostIp": published.HostIP, "HostPort": hostPort}}
	}
	mounts := make([]map[string]any, len(desired.Mounts))
	for index, mount := range desired.Mounts {
		mounts[index] = map[string]any{"Type": "volume", "Source": mount.Source, "Target": mount.Target, "ReadOnly": mount.ReadOnly}
	}
	hostConfig := map[string]any{
		"AutoRemove":     false,
		"CapDrop":        []string{"ALL"},
		"Memory":         desired.MemoryBytes,
		"Mounts":         mounts,
		"NanoCpus":       uint64(desired.CPUMillis) * 1_000_000,
		"NetworkMode":    desired.Network,
		"PortBindings":   bindings,
		"SecurityOpt":    []string{"no-new-privileges:true"},
		"ReadonlyRootfs": false,
	}
	body := map[string]any{
		"Image":        desired.Image,
		"Entrypoint":   desired.Entrypoint,
		"Cmd":          desired.Command,
		"Env":          desired.Environment,
		"Labels":       desired.Labels,
		"ExposedPorts": exposed,
		"HostConfig":   hostConfig,
	}
	var response struct {
		ID string `json:"Id"`
	}
	createPath := "/containers/create?name=" + url.QueryEscape(desired.Name)
	if err := c.call(ctx, http.MethodPost, createPath, body, http.StatusCreated, &response, true); err != nil {
		return ContainerObservation{}, err
	}
	if response.ID == "" {
		return ContainerObservation{}, fmt.Errorf("Docker created a container without an ID")
	}
	if err := c.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(response.ID)+"/start", nil, http.StatusNoContent, nil, true); err != nil {
		_ = c.RemoveContainer(context.WithoutCancel(ctx), response.ID, desired.Labels)
		return ContainerObservation{}, err
	}
	observation, err := c.waitForContainer(ctx, response.ID, desired.Published)
	if err != nil {
		_ = c.RemoveContainer(context.WithoutCancel(ctx), response.ID, desired.Labels)
		return ContainerObservation{}, err
	}
	return observation, nil
}

func (c *EngineClient) waitForContainer(ctx context.Context, id string, published []PublishedPort) (ContainerObservation, error) {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		observation, err := c.InspectContainer(ctx, id)
		if err != nil {
			return ContainerObservation{}, err
		}
		ready := observation.Running
		for _, port := range published {
			hostPort := observation.Ports[fmt.Sprintf("%d/%s", port.ContainerPort, port.Protocol)]
			if hostPort == 0 || !publishedPortReady(ctx, port, hostPort) {
				ready = false
				break
			}
		}
		if ready {
			return observation, nil
		}
		select {
		case <-ctx.Done():
			return ContainerObservation{}, ctx.Err()
		case <-deadline.C:
			return ContainerObservation{}, fmt.Errorf("Docker container did not expose its requested loopback ports before the readiness deadline")
		case <-ticker.C:
		}
	}
}

func publishedPortReady(ctx context.Context, port PublishedPort, hostPort uint16) bool {
	if port.Protocol != "tcp" {
		return true
	}
	probeContext, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(probeContext, "tcp", net.JoinHostPort(port.HostIP, strconv.Itoa(int(hostPort))))
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func (c *EngineClient) InspectContainer(ctx context.Context, id string) (ContainerObservation, error) {
	return c.inspectContainer(ctx, id)
}

func (c *EngineClient) inspectContainer(ctx context.Context, id string) (ContainerObservation, error) {
	var response struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := c.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, http.StatusOK, &response, true); err != nil {
		return ContainerObservation{}, err
	}
	ports := make(map[string]uint16)
	for key, bindings := range response.NetworkSettings.Ports {
		for _, binding := range bindings {
			if binding.HostIP != "127.0.0.1" && binding.HostIP != "::1" {
				return ContainerObservation{}, fmt.Errorf("Docker published %s on a non-loopback address", key)
			}
			parsed, err := strconv.ParseUint(binding.HostPort, 10, 16)
			if err != nil || parsed == 0 {
				return ContainerObservation{}, fmt.Errorf("Docker returned invalid host port for %s", key)
			}
			ports[key] = uint16(parsed)
		}
	}
	return ContainerObservation{ID: response.ID, Running: response.State.Running, Ports: ports, Labels: response.Config.Labels}, nil
}

func (c *EngineClient) RemoveContainer(ctx context.Context, id string, ownership map[string]string) error {
	existing, err := c.inspectContainer(ctx, id)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := verifyLabels(existing.Labels, ownership); err != nil {
		return err
	}
	return c.call(ctx, http.MethodDelete, "/containers/"+url.PathEscape(existing.ID)+"?force=true&v=false", nil, http.StatusNoContent, nil, true)
}

var errNotFound = errors.New("Docker object not found")

func verifyLabels(actual, expected map[string]string) error {
	for key, value := range expected {
		if actual[key] != value {
			return fmt.Errorf("Docker ownership mismatch for label %q", key)
		}
	}
	return nil
}

func (c *EngineClient) call(ctx context.Context, method, requestPath string, body any, expected int, output any, versioned bool) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Docker request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	prefix := ""
	if versioned {
		prefix = c.versionPrefix()
		if prefix == "" {
			if _, err := c.Discover(ctx); err != nil {
				return err
			}
			prefix = c.versionPrefix()
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+prefix+requestPath, payload)
	if err != nil {
		return fmt.Errorf("create Docker request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("Docker Engine request failed: %w", err)
	}
	defer response.Body.Close()
	reader := io.LimitReader(response.Body, maxEngineResponse+1)
	responseBytes, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read Docker Engine response: %w", err)
	}
	if len(responseBytes) > maxEngineResponse {
		return fmt.Errorf("Docker Engine response exceeds %d bytes", maxEngineResponse)
	}
	if response.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if response.StatusCode != expected {
		var apiError struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(responseBytes, &apiError)
		if apiError.Message == "" {
			apiError.Message = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("Docker Engine returned %d: %s", response.StatusCode, apiError.Message)
	}
	if output != nil && len(responseBytes) > 0 {
		if err := json.Unmarshal(responseBytes, output); err != nil {
			return fmt.Errorf("decode Docker Engine response: %w", err)
		}
	}
	return nil
}

func (c *EngineClient) versionPrefix() string {
	prefix := c.apiPrefix.Load()
	if prefix == nil {
		return ""
	}
	return *prefix
}
