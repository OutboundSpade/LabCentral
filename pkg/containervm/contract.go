// Package containervm defines the bounded, provider-independent offline guest runtime contract.
package containervm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const Version = "labcentral.container-vm/v1"
const Entrypoint = "/usr/libexec/labcentral-container-vm"
const MaximumBytes = 32768

var imagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	SchemaVersion  string            `json:"schemaVersion"`
	InstallationID string            `json:"installationId"`
	InstanceID     string            `json:"instanceId"`
	ResourceID     string            `json:"resourceId"`
	Generation     uint64            `json:"generation"`
	Image          string            `json:"image"`
	Environment    map[string]string `json:"environment,omitempty"`
	Command        []string          `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
}

func ValidImage(image string) bool { return len(image) <= 512 && imagePattern.MatchString(image) }
func Decode(data []byte) (Config, error) {
	var c Config
	if len(data) > MaximumBytes {
		return c, fmt.Errorf("runtime configuration too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if d.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("trailing runtime configuration")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.SchemaVersion != Version || !idPattern.MatchString(c.InstallationID) || !idPattern.MatchString(c.InstanceID) || !idPattern.MatchString(c.ResourceID) || c.Generation == 0 || !ValidImage(c.Image) {
		return fmt.Errorf("invalid runtime identity or image")
	}
	if len(c.Environment) > 64 || len(c.Command) > 64 || len(c.Args) > 64 {
		return fmt.Errorf("runtime input exceeds limits")
	}
	for k, v := range c.Environment {
		if !envPattern.MatchString(k) || len(k) > 128 || len(v) > 4096 || strings.ContainsRune(v, 0) {
			return fmt.Errorf("invalid environment literal")
		}
	}
	for _, v := range append(append([]string{}, c.Command...), c.Args...) {
		if len(v) > 4096 || strings.ContainsRune(v, 0) {
			return fmt.Errorf("invalid command argument")
		}
	}
	data, _ := json.Marshal(c)
	if len(data) > MaximumBytes {
		return fmt.Errorf("runtime configuration too large")
	}
	return nil
}

// PodmanCreateArgs never interprets author values as engine flags or host shell code.
// Host networking refers only to this container's dedicated, isolated guest VM.
func (c Config) PodmanCreateArgs(binding string) []string {
	args := []string{"create", "--name", "workload", "--pull=never", "--network=host", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--log-opt=max-size=1mb", "--label", "io.labcentral.binding=" + binding}
	keys := make([]string, 0, len(c.Environment))
	for k := range c.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+c.Environment[k])
	}
	if len(c.Command) > 0 {
		entry, _ := json.Marshal(c.Command)
		args = append(args, "--entrypoint", string(entry))
	}
	args = append(args, c.Image)
	return append(args, c.Args...)
}
