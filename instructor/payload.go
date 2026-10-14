package instructor

import (
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
)

type GuestIdentity struct {
	Address   string `json:"address"`
	PublicKey string `json:"publicKey"`
}
type GuestContext struct {
	Machine            string        `json:"machine"`
	LabExtensions      lc.Extensions `json:"labExtensions"`
	TemplateExtensions lc.Extensions `json:"templateExtensions"`
	OwnerExtensions    lc.Extensions `json:"ownerExtensions"`
}

// GuestFiles executes no author validation: pinned bundles were checked at import.
// It checks only file and instance availability needed for this execution.
func (b Bundle) GuestFiles(src lc.Source, inst lc.Instance, step Step, extensions lc.Extensions) ([]lc.File, error) {
	if inst.Plan.Source.Digest != src.Digest {
		return nil, fmt.Errorf("guest source does not match pinned template")
	}
	present := false
	for _, m := range inst.Plan.Machines {
		if m.Name == step.Machine {
			present = true
		}
	}
	if !present {
		return nil, fmt.Errorf("target machine unavailable in instance")
	}
	files := []lc.File{}
	private := map[string]lc.File{}
	for _, f := range b.Files {
		private[f.Path] = f
	}
	for _, p := range append([]string{step.Playbook}, step.Files...) {
		f, ok := private[p]
		if !ok {
			return nil, fmt.Errorf("pinned instructor file %q unavailable", p)
		}
		files = append(files, f)
	}
	portable := map[string]lc.File{}
	for _, f := range src.Files {
		portable[f.Path] = f
	}
	for _, p := range step.Assets {
		f, ok := portable[p]
		if !ok {
			return nil, fmt.Errorf("pinned asset %q unavailable", p)
		}
		f.Path = "template/" + p
		files = append(files, f)
	}
	if len(step.GuestHosts) > 0 {
		hosts := map[string]GuestIdentity{}
		for _, name := range step.GuestHosts {
			for _, m := range inst.Plan.Machines {
				if m.Name == name {
					hosts[name] = GuestIdentity{m.Address, m.HostPublicKey}
				}
			}
		}
		if len(hosts) != len(step.GuestHosts) {
			return nil, fmt.Errorf("pinned guest identities unavailable")
		}
		data, _ := json.Marshal(hosts)
		files = append(files, lc.File{Path: "labcentral/hosts.json", Content: data})
	}
	data, err := json.Marshal(GuestContext{step.Machine, src.Lab.Extensions, b.Extensions, extensions})
	if err != nil {
		return nil, err
	}
	files = append(files, lc.File{Path: "labcentral/context.json", Content: data})
	return files, nil
}
