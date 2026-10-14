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
	Playbook string   `yaml:"playbook" json:"playbook"`
	Files    []string `yaml:"files,omitempty" json:"files,omitempty"`
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

type AWSImageOptions struct{}
type AWSMachineOptions struct{}
type ProxmoxImageOptions struct{}
type ProxmoxMachineOptions struct{}
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
	ID       string `yaml:"id" json:"id"`
	User     string `yaml:"user" json:"user"`
	DiskGiB  int    `yaml:"diskGiB" json:"diskGiB"`
	Identity string `yaml:"identity" json:"identity"`
}
type AWSConfig struct {
	Region         string         `yaml:"region" json:"region"`
	Zone           string         `yaml:"zone" json:"zone"`
	Account        string         `yaml:"account" json:"account"`
	Profile        string         `yaml:"profile,omitempty" json:"profile,omitempty"`
	Types          []InstanceType `yaml:"types" json:"types"`
	DesktopProfile string         `yaml:"desktopProfile,omitempty" json:"desktopProfile,omitempty"`
}
type InstanceType struct {
	Name      string `yaml:"name" json:"name"`
	CPU       int    `yaml:"cpu" json:"cpu"`
	MemoryMiB int    `yaml:"memoryMiB" json:"memoryMiB"`
}
type ProxmoxConfig struct {
	InsecureSkipTLSVerify bool   `yaml:"insecureSkipTLSVerify,omitempty" json:"insecureSkipTLSVerify,omitempty"`
	URL                   string `yaml:"url" json:"url"`
	TokenID               string `yaml:"tokenID" json:"tokenID"`
	TokenEnv              string `yaml:"tokenEnv" json:"tokenEnv"`
	CAFile                string `yaml:"caFile,omitempty" json:"caFile,omitempty"`
	Node                  string `yaml:"node" json:"node"`
	Storage               string `yaml:"storage" json:"storage"`
	Pool                  string `yaml:"pool" json:"pool"`
	Bridge                string `yaml:"bridge" json:"bridge"`
	VMIDStart             int    `yaml:"vmidStart" json:"vmidStart"`
	VMIDEnd               int    `yaml:"vmidEnd" json:"vmidEnd"`
	VLANStart             int    `yaml:"vlanStart" json:"vlanStart"`
	VLANEnd               int    `yaml:"vlanEnd" json:"vlanEnd"`
	CIDR                  string `yaml:"cidr" json:"cidr"`
	Gateway               string `yaml:"gateway,omitempty" json:"gateway,omitempty"`
	VerifiedIsolation     bool   `yaml:"verifiedIsolation" json:"verifiedIsolation"`
}
type BuildConfig struct {
	DCVURL          string `yaml:"dcvURL,omitempty" json:"dcvURL,omitempty"`
	DCVSHA256       string `yaml:"dcvSHA256,omitempty" json:"dcvSHA256,omitempty"`
	Packer          string `yaml:"packer" json:"packer"`
	PackerSHA256    string `yaml:"packerSHA256" json:"packerSHA256"`
	PluginDirectory string `yaml:"pluginDirectory" json:"pluginDirectory"`
	ToolchainID     string `yaml:"toolchainID" json:"toolchainID"`
	AnsibleVersion  string `yaml:"ansibleVersion" json:"ansibleVersion"`
	TimeoutSeconds  int    `yaml:"timeoutSeconds" json:"timeoutSeconds"`
	SubnetID        string `yaml:"subnetID,omitempty" json:"subnetID,omitempty"`
	SecurityGroupID string `yaml:"securityGroupID,omitempty" json:"securityGroupID,omitempty"`
	InstanceType    string `yaml:"instanceType,omitempty" json:"instanceType,omitempty"`
	Bridge          string `yaml:"bridge,omitempty" json:"bridge,omitempty"`
	VLAN            int    `yaml:"vlan,omitempty" json:"vlan,omitempty"`
	Gateway         string `yaml:"gateway,omitempty" json:"gateway,omitempty"`
}

type HostEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}
type PlannedMachine struct {
	Hosts          []HostEntry `json:"hosts,omitempty"`
	Name           string      `json:"name"`
	Spec           Machine     `json:"spec"`
	NativeImage    string      `json:"nativeImage,omitempty"`
	InstanceType   string      `json:"instanceType,omitempty"`
	CPU            int         `json:"allocatedCPU"`
	MemoryMiB      int         `json:"allocatedMemoryMiB"`
	DiskGiB        int         `json:"diskGiB"`
	Address        string      `json:"address,omitempty"`
	VMID           int         `json:"vmid,omitempty"`
	PublicKey      string      `json:"publicKey,omitempty"`
	HostPrivateKey string      `json:"hostPrivateKey,omitempty"`
	HostPublicKey  string      `json:"hostPublicKey,omitempty"`
}
type PlannedImage struct {
	Name     string `json:"name"`
	Base     Base   `json:"base"`
	Playbook string `json:"playbook,omitempty"`
	CacheKey string `json:"cacheKey"`
	Desktop  bool   `json:"desktop"`
	DiskGiB  int    `json:"diskGiB"`
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
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	Created   time.Time  `json:"created"`
	Plan      Plan       `json:"plan"`
	Resources []Resource `json:"resources"`
	Error     string     `json:"error,omitempty"`
	VLAN      int        `json:"vlan,omitempty"`
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
	ClientPrivateKey []byte         `json:"clientPrivateKey,omitempty"`
	ValidationVMID   int            `json:"validationVMID,omitempty"`
	Action           string         `json:"action"`
	Operation        string         `json:"operation"`
	Owner            string         `json:"owner"`
	Environment      Environment    `json:"environment"`
	Machine          PlannedMachine `json:"machine"`
	Image            PlannedImage   `json:"image"`
	Source           Source         `json:"source"`
	Inputs           []InputValue   `json:"inputs,omitempty"`
	Prior            Resource       `json:"prior"`
	Network          Resource       `json:"network"`
	VLAN             int            `json:"vlan"`
	Recovery         bool           `json:"recovery"`
	Access           AccessOptions  `json:"access"`
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
