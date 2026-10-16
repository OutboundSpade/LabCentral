// Package labcentral implements portable, single-operator virtual labs.
package labcentral

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type Code string

const (
	Invalid           Code = "invalid"
	Unsupported       Code = "unsupported"
	RecoveryRequired  Code = "recovery_required"
	OwnershipMismatch Code = "ownership_mismatch"
	Busy              Code = "busy"
)

type Error struct {
	Code    Code   `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (e *Error) Error() string          { return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Message) }
func problem(c Code, f, m string) error { return &Error{c, f, m} }

type Lab struct {
	Version     int                `yaml:"version" json:"version"`
	Name        string             `yaml:"name" json:"name"`
	Description string             `yaml:"description,omitempty" json:"description,omitempty"`
	Images      map[string]Image   `yaml:"images" json:"images"`
	Machines    map[string]Machine `yaml:"machines" json:"machines"`
	Inputs      map[string]Input   `yaml:"inputs,omitempty" json:"inputs,omitempty" proto:"7"`
	Platforms   *PlatformList      `yaml:"platforms,omitempty" json:"platforms,omitempty" proto:"8"`
	Extensions  Extensions         `yaml:"extensions,omitempty" json:"extensions,omitempty" proto:"9"`
	Networks    map[string]Network `yaml:"networks,omitempty" json:"networks,omitempty" proto:"10"`
}

// PlatformList distinguishes an omitted restriction from an explicit empty set.
type PlatformList struct {
	Names []string `json:"names"`
}
type Image struct {
	Base       string        `yaml:"base" json:"base"`
	OS         string        `yaml:"os,omitempty" json:"os,omitempty" proto:"4"`
	Build      *ImageBuild   `yaml:"build,omitempty" json:"build,omitempty" proto:"5"`
	Platform   ImagePlatform `yaml:"platform,omitempty" json:"platform,omitempty" proto:"6"`
	Extensions Extensions    `yaml:"extensions,omitempty" json:"extensions,omitempty" proto:"7"`
}
type ImageBuild struct {
	Playbook string   `yaml:"playbook,omitempty" json:"playbook"`
	Script   string   `yaml:"script,omitempty" json:"script,omitempty" proto:"3"`
	Files    []string `yaml:"files,omitempty" json:"files,omitempty" proto:"2"`
}

func (i Image) BuildPlaybook() string {
	if i.Build == nil {
		return ""
	}
	return i.Build.Playbook
}
func (i Image) BuildFiles() []string {
	if i.Build == nil {
		return nil
	}
	return i.Build.Files
}

type AWSImageOptions struct {
	BuildInstanceType string `yaml:"buildInstanceType,omitempty" json:"buildInstanceType,omitempty"`
}
type AWSMachineOptions struct {
	InstanceType string            `yaml:"instanceType,omitempty" json:"instanceType,omitempty"`
	Volume       *AWSVolumeOptions `yaml:"volume,omitempty" json:"volume,omitempty"`
	CPUCredits   string            `yaml:"cpuCredits,omitempty" json:"cpuCredits,omitempty"`
}
type AWSVolumeOptions struct {
	Type       string `yaml:"type,omitempty" json:"type,omitempty"`
	IOPS       *int   `yaml:"iops,omitempty" json:"iops,omitempty"`
	Throughput *int   `yaml:"throughput,omitempty" json:"throughput,omitempty"`
}
type ProxmoxImageOptions struct{}
type ProxmoxMachineOptions struct {
	CPUType       string `yaml:"cpuType,omitempty" json:"cpuType,omitempty"`
	NICModel      string `yaml:"nicModel,omitempty" json:"nicModel,omitempty"`
	MemoryBalloon *bool  `yaml:"memoryBalloon,omitempty" json:"memoryBalloon,omitempty"`
}
type ImagePlatform struct {
	AWS     *AWSImageOptions     `yaml:"aws,omitempty" json:"aws,omitempty"`
	Proxmox *ProxmoxImageOptions `yaml:"proxmox,omitempty" json:"proxmox,omitempty"`
}
type MachinePlatform struct {
	AWS     *AWSMachineOptions     `yaml:"aws,omitempty" json:"aws,omitempty"`
	Proxmox *ProxmoxMachineOptions `yaml:"proxmox,omitempty" json:"proxmox,omitempty"`
}
type Machine struct {
	Image         string          `yaml:"image" json:"image"`
	CPU           int             `yaml:"cpu" json:"cpu"`
	Memory        string          `yaml:"memory" json:"memory"`
	Disk          string          `yaml:"disk" json:"disk"`
	Access        []string        `yaml:"access,omitempty" json:"access,omitempty"`
	NICs          []NIC           `yaml:"nics,omitempty" json:"nics,omitempty" proto:"7"`
	Forwarding    bool            `yaml:"forwarding,omitempty" json:"forwarding,omitempty" proto:"8"`
	HostnamePeers HostnamePeers   `yaml:"hostnamePeers,omitempty" json:"hostnamePeers" proto:"9"`
	Platform      MachinePlatform `yaml:"platform,omitempty" json:"platform,omitempty" proto:"10"`
	Extensions    Extensions      `yaml:"extensions,omitempty" json:"extensions,omitempty" proto:"11"`
}

// Explicit distinguishes an empty peer list from the default all-peer inventory.
type HostnamePeers struct {
	Explicit bool     `json:"explicit"`
	Names    []string `json:"names,omitempty"`
}
type NIC struct {
	Network string `yaml:"network" json:"network"`
	Address *int   `yaml:"address,omitempty" json:"address,omitempty"`
}
type Network struct {
	Egress     string     `yaml:"egress,omitempty" json:"egress,omitempty" proto:"2"`
	CIDR       string     `yaml:"cidr,omitempty" json:"cidr,omitempty" proto:"3"`
	Extensions Extensions `yaml:"extensions,omitempty" json:"extensions,omitempty" proto:"4"`
}
type Input struct {
	Type        string    `yaml:"type" json:"type"`
	Required    bool      `yaml:"required,omitempty" json:"required,omitempty" proto:"3"`
	Default     JSONValue `yaml:"default,omitempty" json:"default,omitempty" proto:"4"`
	Description string    `yaml:"description,omitempty" json:"description,omitempty" proto:"5"`
}
type InputValue struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}
type File struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}
type Source struct {
	Lab    Lab    `json:"lab"`
	Files  []File `json:"files"`
	Digest string `json:"digest"`
}

type Environment struct {
	Version       int             `yaml:"version" json:"version"`
	Name          string          `yaml:"name" json:"name"`
	Platform      string          `yaml:"platform" json:"platform"`
	OperatorCIDRs []string        `yaml:"operatorCIDRs" json:"operatorCIDRs"`
	Bases         map[string]Base `yaml:"bases" json:"bases"`
	AWS           AWSConfig       `yaml:"aws,omitempty" json:"aws,omitempty"`
	Proxmox       ProxmoxConfig   `yaml:"proxmox,omitempty" json:"proxmox,omitempty"`
	Build         BuildConfig     `yaml:"build" json:"build"`
}
type Base struct {
	OS       string `yaml:"os,omitempty" json:"os,omitempty" proto:"5"`
	ID       string `yaml:"id" json:"id" proto:"1"`
	User     string `yaml:"user" json:"user" proto:"2"`
	DiskGiB  int    `yaml:"diskGiB" json:"diskGiB" proto:"3"`
	Identity string `yaml:"identity" json:"identity" proto:"4"`
}
type AWSGatewayConfig struct {
	Base         string `yaml:"base,omitempty" json:"base,omitempty"`
	InstanceType string `yaml:"instanceType,omitempty" json:"instanceType,omitempty"`
}

func (g AWSGatewayConfig) Effective() AWSGatewayConfig {
	if g.Base == "" {
		g.Base = "ubuntu-24.04"
	}
	if g.InstanceType == "" {
		g.InstanceType = "t3.micro"
	}
	return g
}

type AWSConfig struct {
	Region         string           `yaml:"region" json:"region"`
	Zone           string           `yaml:"zone" json:"zone"`
	Account        string           `yaml:"account" json:"account"`
	Profile        string           `yaml:"profile,omitempty" json:"profile,omitempty"`
	Types          []InstanceType   `yaml:"types" json:"types"`
	DesktopProfile string           `yaml:"desktopProfile,omitempty" json:"desktopProfile,omitempty"`
	AllowEgress    bool             `yaml:"allowEgress,omitempty" json:"allowEgress,omitempty"`
	Gateway        AWSGatewayConfig `yaml:"gateway,omitempty" json:"gateway,omitempty,omitzero"`
}
type InstanceType struct {
	Name      string `yaml:"name" json:"name"`
	CPU       int    `yaml:"cpu" json:"cpu"`
	MemoryMiB int    `yaml:"memoryMiB" json:"memoryMiB"`
	MaxNICs   int    `yaml:"maxNICs,omitempty" json:"maxNICs,omitempty"`
}
type ProxmoxManagementAddresses struct {
	CIDR    string   `yaml:"cidr" json:"cidr"`
	Gateway string   `yaml:"gateway" json:"gateway"`
	DNS     []string `yaml:"dns" json:"dns"`
}
type ProxmoxConfig struct {
	InsecureSkipTLSVerify bool                        `yaml:"insecureSkipTLSVerify,omitempty" json:"insecureSkipTLSVerify,omitempty" proto:"1"`
	URL                   string                      `yaml:"url" json:"url" proto:"2"`
	TokenID               string                      `yaml:"tokenID" json:"tokenID" proto:"3"`
	TokenEnv              string                      `yaml:"tokenEnv" json:"tokenEnv" proto:"4"`
	CAFile                string                      `yaml:"caFile,omitempty" json:"caFile,omitempty" proto:"5"`
	Node                  string                      `yaml:"node" json:"node" proto:"6"`
	Storage               string                      `yaml:"storage" json:"storage" proto:"7"`
	Pool                  string                      `yaml:"pool" json:"pool" proto:"8"`
	LabBridge             string                      `yaml:"labBridge" json:"labBridge" proto:"19"`
	VMIDStart             int                         `yaml:"vmidStart" json:"vmidStart" proto:"10"`
	VMIDEnd               int                         `yaml:"vmidEnd" json:"vmidEnd" proto:"11"`
	VLANStart             int                         `yaml:"vlanStart" json:"vlanStart" proto:"12"`
	VLANEnd               int                         `yaml:"vlanEnd" json:"vlanEnd" proto:"13"`
	CIDR                  string                      `yaml:"cidr" json:"cidr" proto:"14"`
	CPUTypes              []string                    `yaml:"cpuTypes,omitempty" json:"cpuTypes,omitempty" proto:"17"`
	AllowEgress           bool                        `yaml:"allowEgress,omitempty" json:"allowEgress,omitempty" proto:"18"`
	ManagementBridge      string                      `yaml:"managementBridge" json:"managementBridge" proto:"20"`
	ManagementAddresses   *ProxmoxManagementAddresses `yaml:"managementAddresses,omitempty" json:"managementAddresses,omitempty" proto:"21"`
	GatewayBase           string                      `yaml:"gatewayBase,omitempty" json:"gatewayBase,omitempty" proto:"22"`
}

func (p ProxmoxConfig) EffectiveGatewayBase() string {
	if p.GatewayBase == "" {
		return "ubuntu-24.04"
	}
	return p.GatewayBase
}

type BuildConfig struct {
	DCVURL           string `yaml:"dcvURL,omitempty" json:"dcvURL,omitempty"`
	DCVSHA256        string `yaml:"dcvSHA256,omitempty" json:"dcvSHA256,omitempty"`
	Packer           string `yaml:"packer" json:"packer"`
	PackerSHA256     string `yaml:"packerSHA256" json:"packerSHA256"`
	PluginDirectory  string `yaml:"pluginDirectory" json:"pluginDirectory"`
	ToolchainID      string `yaml:"toolchainID" json:"toolchainID"`
	AnsibleVersion   string `yaml:"ansibleVersion" json:"ansibleVersion"`
	TimeoutSeconds   int    `yaml:"timeoutSeconds" json:"timeoutSeconds"`
	SubnetID         string `yaml:"subnetID,omitempty" json:"subnetID,omitempty"`
	SecurityGroupID  string `yaml:"securityGroupID,omitempty" json:"securityGroupID,omitempty"`
	InstanceType     string `yaml:"instanceType,omitempty" json:"instanceType,omitempty"`
	Bridge           string `yaml:"bridge,omitempty" json:"bridge,omitempty"`
	VLAN             int    `yaml:"vlan,omitempty" json:"vlan,omitempty"`
	Gateway          string `yaml:"gateway,omitempty" json:"gateway,omitempty"`
	WindowsDCVURL    string `yaml:"windowsDCVURL,omitempty" json:"windowsDCVURL,omitempty"`
	WindowsDCVSHA256 string `yaml:"windowsDCVSHA256,omitempty" json:"windowsDCVSHA256,omitempty"`
}

type HostEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// PlannedNetwork describes one isolated segment; Routes use a router NIC on it.
type PlannedNetwork struct {
	Name    string         `json:"name"`
	CIDR    string         `json:"cidr"`
	Gateway string         `json:"gateway,omitempty"`
	Egress  string         `json:"egress"`
	VLAN    int            `json:"vlan,omitempty"`
	Routes  []PlannedRoute `json:"routes,omitempty"`
}
type PlannedRoute struct {
	Network   string `json:"network"`
	CIDR      string `json:"cidr"`
	Via       string `json:"via"`
	Router    string `json:"router"`
	RouterNIC int    `json:"routerNIC"`
}
type PlannedNIC struct {
	Network string         `json:"network"`
	CIDR    string         `json:"cidr"`
	Address string         `json:"address"`
	VLAN    int            `json:"vlan,omitempty"`
	Gateway string         `json:"gateway,omitempty"`
	Default bool           `json:"default,omitempty"`
	Routes  []PlannedRoute `json:"routes,omitempty"`
}
type PlannedMachine struct {
	Hosts          []HostEntry  `json:"hosts,omitempty"`
	Name           string       `json:"name"`
	Spec           Machine      `json:"spec"`
	NativeImage    string       `json:"nativeImage,omitempty"`
	InstanceType   string       `json:"instanceType,omitempty"`
	CPU            int          `json:"allocatedCPU"`
	MemoryMiB      int          `json:"allocatedMemoryMiB"`
	DiskGiB        int          `json:"diskGiB"`
	Address        string       `json:"address,omitempty"`
	VMID           int          `json:"vmid,omitempty"`
	PublicKey      string       `json:"publicKey,omitempty"`
	HostPrivateKey string       `json:"hostPrivateKey,omitempty"`
	HostPublicKey  string       `json:"hostPublicKey,omitempty"`
	NICs           []PlannedNIC `json:"nics,omitempty"`
	Resolver       string       `json:"resolver,omitempty"`
	OS             string       `json:"os,omitempty"`
}
type PlannedImage struct {
	Name              string `json:"name"`
	Base              Base   `json:"base"`
	Playbook          string `json:"playbook,omitempty"`
	CacheKey          string `json:"cacheKey"`
	Desktop           bool   `json:"desktop"`
	DiskGiB           int    `json:"diskGiB"`
	BuildInstanceType string `json:"buildInstanceType,omitempty"`
	Script            string `json:"script,omitempty"`
}
type Plan struct {
	Version           int              `json:"version"`
	Source            Source           `json:"source"`
	Environment       Environment      `json:"environment"`
	EnvironmentDigest string           `json:"environmentDigest"`
	Images            []PlannedImage   `json:"images"`
	Machines          []PlannedMachine `json:"machines"`
	Inputs            []InputValue     `json:"inputs,omitempty"`
	Notices           []string         `json:"notices,omitempty"`
	Networks          []PlannedNetwork `json:"networks,omitempty"`
	VPCCIDR           string           `json:"vpcCIDR,omitempty"`
	EdgeCIDR          string           `json:"edgeCIDR,omitempty"`
}
type Resource struct {
	Kind           string           `json:"kind"`
	Name           string           `json:"name"`
	ID             string           `json:"id"`
	Owner          string           `json:"owner"`
	Address        string           `json:"address,omitempty"`
	PrivateAddress string           `json:"privateAddress,omitempty"`
	Children       []NativeResource `json:"children,omitempty"`
	Complete       bool             `json:"complete"`
	Data           json.RawMessage  `json:"data,omitempty"`
}
type NativeResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type Instance struct {
	// Gateway is absent on retained pre-gateway deployments.
	Gateway   *PlannedMachine `json:"gateway,omitempty"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	Created   time.Time       `json:"created"`
	Plan      Plan            `json:"plan"`
	Resources []Resource      `json:"resources"`
	Error     string          `json:"error,omitempty"`
	VLAN      int             `json:"vlan,omitempty"`
}
type ImageRecord struct {
	ID                string      `json:"id"`
	Key               string      `json:"key"`
	Name              string      `json:"name"`
	EnvironmentDigest string      `json:"environmentDigest"`
	Resource          Resource    `json:"resource"`
	Created           time.Time   `json:"created"`
	Environment       Environment `json:"environment"`
}
type Event struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Stage     string `json:"stage"`
	Resource  string `json:"resource,omitempty"`
	Message   string `json:"message,omitempty"`
}
type Observer func(Event)

// Command is a closed union selected by Action. Platform state is opaque bytes,
// never a portable resource map. Credentials are resolved only by the adapter.
type Command struct {
	ClientPrivateKey []byte           `json:"clientPrivateKey,omitempty"`
	ValidationVMID   int              `json:"validationVMID,omitempty"`
	Action           string           `json:"action"`
	Operation        string           `json:"operation"`
	Owner            string           `json:"owner"`
	Environment      Environment      `json:"environment"`
	Machine          PlannedMachine   `json:"machine"`
	Image            PlannedImage     `json:"image"`
	Source           Source           `json:"source"`
	Inputs           []InputValue     `json:"inputs,omitempty"`
	Prior            Resource         `json:"prior"`
	Network          Resource         `json:"network"`
	VLAN             int              `json:"vlan"`
	Recovery         bool             `json:"recovery"`
	Access           AccessOptions    `json:"access"`
	Gateway          Resource         `json:"gateway,omitempty"`
	Networks         []PlannedNetwork `json:"networks,omitempty"`
	GatewayVMID      int              `json:"gatewayVMID,omitempty"`
}
type Result struct {
	Resource Resource `json:"resource"`
	Absent   bool     `json:"absent,omitempty"`
	Message  string   `json:"message,omitempty"`
}
type Provider interface {
	Execute(context.Context, Command, Observer) (Result, error)
}

func Contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
