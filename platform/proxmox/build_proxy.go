package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/build"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/nativeproxy"
)

func (p *Provider) BuildProxy(ctx context.Context, cmd lc.Command, cfg *build.Config) (func(), error) {
	client, err := NewClient(cmd.Environment)
	if err != nil {
		return nil, err
	}
	upstream, err := url.Parse(cmd.Environment.Proxmox.URL)
	if err != nil {
		return nil, err
	}
	if err = p.Journal.Save(cmd.Owner+"/guarded-build", true); err != nil {
		return nil, err
	}
	guard := &proxmoxBuildGuard{p: p, api: client, c: cmd}
	endpoint, close, err := nativeproxy.Start(faultinject.WithOperation(ctx, "proxmox", "image.build"), nativeproxy.Config{
		Upstream: upstream, Protocol: "proxmox", Transport: client.HTTP.Transport, Do: guard.do, Verify: func(ctx context.Context, r *http.Request, b []byte) error {
			if r.Method == "GET" || strings.HasSuffix(r.URL.Path, "/clone") {
				return nil
			}
			v, err := client.Config(ctx, cmd.Machine.VMID)
			if err != nil {
				return err
			}
			if err = checkOwner(v, cmd.Owner, cmd.Image.Name); err != nil {
				return err
			}
			if v.Lock != "" {
				return fmt.Errorf("native build task is locked")
			}
			return nil
		},
		Step: func(r *http.Request, _ []byte) string { return "packer." + faultStep(r.Method, r.URL.Path) },
		Before: func(ctx context.Context, r *http.Request, body []byte) ([]byte, error) {
			return p.guardBuildRequest(ctx, client, cmd, r, body)
		},
	})
	if err != nil {
		return nil, err
	}
	var native map[string]any
	if err = json.Unmarshal(cfg.Builders[0], &native); err != nil {
		close()
		return nil, err
	}
	native["proxmox_url"] = endpoint + "/api2/json"
	cfg.Builders[0], err = json.Marshal(native)
	if err != nil {
		close()
		return nil, err
	}
	return close, nil
}
func (p *Provider) guardBuildRequest(ctx context.Context, c *Client, cmd lc.Command, r *http.Request, body []byte) ([]byte, error) {
	if r.Method == "GET" {
		return body, nil
	}
	prefix := "/api2/json/nodes/" + cmd.Environment.Proxmox.Node + "/qemu/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return nil, fmt.Errorf("builder mutation is outside reserved VM scope")
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, err
	}
	suffix := ""
	if len(parts) > 1 {
		suffix = "/" + parts[1]
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	description := marker(cmd.Owner, cmd.Image.Name)
	if suffix == "/clone" && r.Method == "POST" {
		base, err := strconv.Atoi(cmd.Image.Base.ID)
		if err != nil {
			return nil, err
		}
		if id != base || values.Get("newid") != strconv.Itoa(cmd.Machine.VMID) {
			return nil, fmt.Errorf("builder clone differs from reserved IDs")
		}
		v, err := c.Config(ctx, base)
		if err != nil {
			return nil, err
		}
		if v.Template != 1 || v.Digest != cmd.Image.Base.Identity {
			return nil, fmt.Errorf("builder base identity changed")
		}
		values.Set("description", description)
		values.Set("pool", cmd.Environment.Proxmox.Pool)
		return []byte(values.Encode()), nil
	}
	if err = p.reconcileBuildMutations(ctx, c, cmd); err != nil {
		return nil, err
	}
	if id != cmd.Machine.VMID {
		return nil, fmt.Errorf("builder mutation is outside reserved VMID")
	}
	v, err := c.Config(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = checkOwner(v, cmd.Owner, cmd.Image.Name); err != nil {
		return nil, err
	}
	if v.Lock != "" {
		return nil, fmt.Errorf("recovery_required: native build task is still locked")
	}
	if err = p.rememberDisks(ctx, c, cmd, id, v); err != nil {
		return nil, err
	}
	switch {
	case suffix == "/config" && (r.Method == "PUT" || r.Method == "POST"):
		// Packer normally substitutes a generic ephemeral description. Preserve the
		// atomic clone owner instead, so every later cleanup can verify the VM.
		values.Set("description", description)
		if tags := values.Get("tags"); tags != "" {
			parts := strings.Split(tags, ";")
			if !lc.Contains(parts, "labcentral-v2") || !lc.Contains(parts, cmd.Owner) {
				return nil, fmt.Errorf("builder cannot replace ownership tags")
			}
		}
		return []byte(values.Encode()), nil
	case (suffix == "/status/start" || suffix == "/status/stop" || suffix == "/status/shutdown" || suffix == "/template") && r.Method == "POST":
		return body, nil
	case suffix == "" && r.Method == "DELETE":
		if err = p.reconcileBuildMutations(ctx, c, cmd); err != nil {
			return nil, err
		}
		query := r.URL.Query()
		if query.Get("purge") == "1" || query.Get("purge") == "true" || values.Get("purge") == "1" || values.Get("purge") == "true" {
			return nil, fmt.Errorf("builder cannot purge shared VM configuration")
		}
		if values.Get("destroy-unreferenced-disks") != "" || query.Get("destroy-unreferenced-disks") != "" {
			return nil, fmt.Errorf("builder cannot purge unreferenced disks")
		}
		return body, nil
	default:
		return nil, fmt.Errorf("unsupported builder mutation in reserved VM")
	}
}

func (p *Provider) checkBuildOwner(v VMConfig, cmd lc.Command) error {
	if v.Description == marker(cmd.Owner, cmd.Image.Name) {
		return nil
	}
	var guarded bool
	err := p.Journal.Load(cmd.Owner+"/guarded-build", &guarded)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Retain the pinned plugin's one historical placeholder only for a build
	// started before the guarded clone policy, and only with both exact tags.
	if !guarded && v.Template != 1 && v.Description == "Packer ephemeral build VM" && lc.Contains(strings.Split(v.Tags, ";"), "labcentral-v2") && lc.Contains(strings.Split(v.Tags, ";"), cmd.Owner) {
		return nil
	}
	return fmt.Errorf("ownership_mismatch: native build VM retained")
}
