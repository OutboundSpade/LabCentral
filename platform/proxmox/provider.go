// Package proxmox implements a bounded native Proxmox VM adapter.
package proxmox

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/guest"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Provider struct {
	Abandon func(context.Context, lc.Command, lc.Observer) (lc.Result, error)
	Journal journal.Journal
	Build   func(context.Context, lc.Command, lc.Observer) (lc.Result, error)
}
type Client struct {
	URL, Token string
	HTTP       *http.Client
	Node       string
}

func NewClient(e lc.Environment) (*Client, error) {
	p := e.Proxmox
	secret := os.Getenv(p.TokenEnv)
	if secret == "" {
		return nil, fmt.Errorf("Proxmox token environment variable is unset")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}
	if p.CAFile != "" {
		b, err := os.ReadFile(p.CAFile)
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("invalid Proxmox CA")
		}
	}
	return &Client{URL: strings.TrimRight(p.URL, "/") + "/api2/json", Token: p.TokenID + "=" + secret, Node: p.Node, HTTP: &http.Client{Timeout: 90 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, InsecureSkipVerify: p.InsecureSkipTLSVerify}}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirect refused") }}}, nil
}
func (c *Client) Call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.Token)
	req.Header.Set("Content-Type", "application/json")
	res, e := c.HTTP.Do(req)
	if e != nil {
		return fmt.Errorf("Proxmox transport: %w", e)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &HTTPError{res.StatusCode, method, path}
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if e != nil {
		return e
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if e = json.Unmarshal(b, &envelope); e != nil {
		return e
	}
	if out != nil {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

type HTTPError struct {
	Status       int
	Method, Path string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Proxmox %s %s returned HTTP %d", e.Method, e.Path, e.Status)
}
func (c *Client) VM(id int, suffix string) string {
	return "/nodes/" + url.PathEscape(c.Node) + "/qemu/" + strconv.Itoa(id) + suffix
}
func (c *Client) Wait(ctx context.Context, upid string) error {
	if !strings.HasPrefix(upid, "UPID:") {
		return fmt.Errorf("invalid task identity")
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		var s struct {
			Status string `json:"status"`
			Exit   string `json:"exitstatus"`
		}
		if e := c.Call(ctx, "GET", "/nodes/"+url.PathEscape(c.Node)+"/tasks/"+url.PathEscape(upid)+"/status", nil, &s); e != nil {
			return e
		}
		if s.Status == "stopped" {
			if s.Exit != "OK" {
				return fmt.Errorf("Proxmox task failed: %s", s.Exit)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type VMConfig struct {
	Tags        string `json:"tags"`
	Description string `json:"description"`
	Digest      string `json:"digest"`
	Template    int    `json:"template"`
	Name        string `json:"name"`
	Net0        string `json:"net0"`
}

func marker(owner, name string) string { return "labcentral:v2:" + owner + ":" + name }
func (c *Client) Config(ctx context.Context, id int) (VMConfig, error) {
	var v VMConfig
	e := c.Call(ctx, "GET", c.VM(id, "/config"), nil, &v)
	return v, e
}
func checkOwner(v VMConfig, owner, name string) error {
	if v.Description != marker(owner, name) {
		return fmt.Errorf("ownership_mismatch: Proxmox VM")
	}
	return nil
}
func (p *Provider) Execute(ctx context.Context, cmd lc.Command, o lc.Observer) (lc.Result, error) {
	if cmd.Environment.Platform != "proxmox" {
		return lc.Result{}, fmt.Errorf("wrong platform")
	}
	c, e := NewClient(cmd.Environment)
	if e != nil {
		return lc.Result{}, e
	}
	switch cmd.Action {
	case "check":
		return p.check(ctx, c, cmd)
	case "network.create":
		cidr, gw, e := lc.ProxmoxSubnet(cmd.Environment.Proxmox, cmd.VLAN)
		if e != nil {
			return lc.Result{}, e
		}
		b, _ := json.Marshal(network{CIDR: cidr, Gateway: gw, VLAN: cmd.VLAN})
		return lc.Result{Resource: lc.Resource{Kind: "network", Name: "private", ID: strconv.Itoa(cmd.VLAN), Owner: cmd.Owner, Complete: true, Data: b}}, nil
	case "machine.create":
		return p.machine(ctx, c, cmd)
	case "discover":
		if cmd.Machine.Name == "" {
			return lc.Result{Resource: lc.Resource{Kind: "network", Name: "private", ID: strconv.Itoa(cmd.VLAN), Owner: cmd.Owner}}, nil
		}
		return p.find(ctx, c, cmd)
	case "resource.destroy":
		return p.destroy(ctx, c, cmd)
	case "image.read":
		id, e := strconv.Atoi(cmd.Prior.ID)
		if e != nil {
			return lc.Result{}, e
		}
		v, e := c.Config(ctx, id)
		if e != nil {
			return lc.Result{}, e
		}
		if e = checkOwner(v, cmd.Owner, cmd.Prior.Name); e != nil {
			return lc.Result{}, e
		}
		r := cmd.Prior
		r.Complete = v.Template == 1
		return lc.Result{Resource: r}, nil
	case "image.abandon":
		if p.Abandon == nil {
			return lc.Result{}, fmt.Errorf("builder unavailable")
		}
		return p.Abandon(ctx, cmd, o)
	case "image.build":
		if p.Build == nil {
			return lc.Result{}, fmt.Errorf("builder unavailable")
		}
		return p.Build(ctx, cmd, o)
	}
	return lc.Result{}, fmt.Errorf("unsupported Proxmox action %q", cmd.Action)
}
func (p *Provider) check(ctx context.Context, c *Client, cmd lc.Command) (lc.Result, error) {
	var version struct {
		Version string `json:"version"`
	}
	if e := c.Call(ctx, "GET", "/version", nil, &version); e != nil {
		return lc.Result{}, e
	}
	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if e := c.Call(ctx, "GET", "/nodes", nil, &nodes); e != nil {
		return lc.Result{}, e
	}
	found := false
	for _, n := range nodes {
		found = found || n.Node == c.Node && n.Status == "online"
	}
	if !found {
		return lc.Result{}, fmt.Errorf("configured node is not online")
	}
	var firewall struct {
		Enable int `json:"enable"`
	}
	if err := c.Call(ctx, "GET", "/cluster/firewall/options", nil, &firewall); err != nil {
		return lc.Result{}, err
	}
	if firewall.Enable != 1 {
		return lc.Result{}, fmt.Errorf("Proxmox cluster firewall is disabled; VM firewall rules cannot enforce the lab policy")
	}
	var bridges []struct {
		Interface string `json:"iface"`
		VLANAware int    `json:"bridge_vlan_aware"`
	}
	if err := c.Call(ctx, "GET", "/nodes/"+url.PathEscape(c.Node)+"/network", nil, &bridges); err != nil {
		return lc.Result{}, err
	}
	bridgeOK := false
	for _, b := range bridges {
		if b.Interface == cmd.Environment.Proxmox.Bridge && b.VLANAware == 1 {
			bridgeOK = true
		}
	}
	if !bridgeOK {
		return lc.Result{}, fmt.Errorf("lab bridge must exist and be VLAN-aware")
	}
	var pool struct {
		PoolID string `json:"poolid"`
	}
	if err := c.Call(ctx, "GET", "/pools/"+url.PathEscape(cmd.Environment.Proxmox.Pool), nil, &pool); err != nil {
		return lc.Result{}, err
	}
	for _, base := range cmd.Environment.Bases {
		id, e := strconv.Atoi(base.ID)
		if e != nil {
			return lc.Result{}, e
		}
		v, e := c.Config(ctx, id)
		if e != nil {
			return lc.Result{}, e
		}
		if v.Template != 1 || v.Digest != base.Identity {
			return lc.Result{}, fmt.Errorf("base template identity changed")
		}
	}
	return lc.Result{Message: "node and base templates verified; physical network isolation is operator-owned"}, nil
}

type network struct {
	CIDR    string `json:"cidr"`
	Gateway string `json:"gateway"`
	VLAN    int    `json:"vlan"`
}

func (p *Provider) machine(ctx context.Context, c *Client, cmd lc.Command) (lc.Result, error) {
	m := cmd.Machine
	var net network
	if e := json.Unmarshal(cmd.Network.Data, &net); e != nil {
		return lc.Result{}, e
	}
	base, e := strconv.Atoi(m.NativeImage)
	if e != nil {
		return lc.Result{}, e
	}
	_, e = journal.Do(ctx, p.Journal, cmd.Operation+"/clone", func() (int, error) {
		var task string
		in := struct {
			NewID       int    `json:"newid"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Full        int    `json:"full"`
			Storage     string `json:"storage"`
			Pool        string `json:"pool"`
		}{m.VMID, "lc-" + cmd.Owner[:12] + "-" + m.Name, marker(cmd.Owner, m.Name), 1, cmd.Environment.Proxmox.Storage, cmd.Environment.Proxmox.Pool}
		if e := c.Call(ctx, "POST", c.VM(base, "/clone"), in, &task); e != nil {
			return 0, e
		}
		if e := c.Wait(ctx, task); e != nil {
			return 0, e
		}
		return m.VMID, nil
	}, func() (int, bool, error) {
		v, e := c.Config(ctx, m.VMID)
		if e != nil {
			return 0, false, e
		}
		if e = checkOwner(v, cmd.Owner, m.Name); e != nil {
			return 0, false, e
		}
		return m.VMID, true, nil
	})
	if e != nil {
		return lc.Result{}, e
	}
	v, e := c.Config(ctx, m.VMID)
	if e != nil {
		return lc.Result{}, e
	}
	if e = checkOwner(v, cmd.Owner, m.Name); e != nil {
		return lc.Result{}, e
	}
	ip := "ip=" + m.Address + "/" + strings.Split(net.CIDR, "/")[1]
	if net.Gateway != "" {
		ip += ",gw=" + net.Gateway
	}
	nic := strings.Split(v.Net0, ",")[0]
	if !strings.HasPrefix(nic, "virtio=") {
		nic = "virtio"
	}
	cfg := struct {
		Cores  int    `json:"cores"`
		Memory int    `json:"memory"`
		Net0   string `json:"net0"`
		IP     string `json:"ipconfig0"`
		User   string `json:"ciuser"`
		Keys   string `json:"sshkeys"`
		Agent  string `json:"agent"`
		Digest string `json:"digest"`
	}{m.CPU, m.MemoryMiB, fmt.Sprintf("%s,bridge=%s,tag=%d,firewall=1", nic, cmd.Environment.Proxmox.Bridge, cmd.VLAN), ip, "lab", url.QueryEscape(m.PublicKey), "enabled=1", v.Digest}
	if e = c.Call(ctx, "PUT", c.VM(m.VMID, "/config"), cfg, nil); e != nil {
		return lc.Result{}, e
	}
	if e = c.Call(ctx, "PUT", c.VM(m.VMID, "/resize"), struct {
		Disk string `json:"disk"`
		Size string `json:"size"`
	}{"scsi0", fmt.Sprintf("%dG", m.DiskGiB)}, nil); e != nil {
		return lc.Result{}, e
	}
	// Reconcile only rules belonging to the exact owned VM. Rule ordering is fixed.
	var rules []struct {
		Pos int `json:"pos"`
	}
	if e = c.Call(ctx, "GET", c.VM(m.VMID, "/firewall/rules"), nil, &rules); e != nil {
		return lc.Result{}, e
	}
	for n := len(rules) - 1; n >= 0; n-- {
		if e = c.Call(ctx, "DELETE", c.VM(m.VMID, "/firewall/rules/"+strconv.Itoa(rules[n].Pos)), nil, nil); e != nil {
			return lc.Result{}, e
		}
	}
	type rule struct {
		Type   string `json:"type"`
		Action string `json:"action"`
		Enable int    `json:"enable"`
		Source string `json:"source,omitempty"`
		Dest   string `json:"dest,omitempty"`
		Proto  string `json:"proto,omitempty"`
		Dport  string `json:"dport,omitempty"`
	}
	desired := []rule{{Type: "in", Action: "ACCEPT", Enable: 1, Source: net.CIDR}, {Type: "out", Action: "ACCEPT", Enable: 1, Dest: net.CIDR}}
	for _, cidr := range cmd.Environment.OperatorCIDRs {
		desired = append(desired, rule{Type: "in", Action: "ACCEPT", Enable: 1, Source: cidr, Proto: "tcp", Dport: "22"})
	}
	for _, r := range desired {
		if e = c.Call(ctx, "POST", c.VM(m.VMID, "/firewall/rules"), r, nil); e != nil {
			return lc.Result{}, e
		}
	}
	opts := struct {
		Enable int    `json:"enable"`
		In     string `json:"policy_in"`
		Out    string `json:"policy_out"`
	}{1, "DROP", "DROP"}
	if e = c.Call(ctx, "PUT", c.VM(m.VMID, "/firewall/options"), opts, nil); e != nil {
		return lc.Result{}, e
	}
	var state struct {
		Status string `json:"status"`
	}
	if e = c.Call(ctx, "GET", c.VM(m.VMID, "/status/current"), nil, &state); e != nil {
		return lc.Result{}, e
	}
	if state.Status != "running" {
		var task string
		if e = c.Call(ctx, "POST", c.VM(m.VMID, "/status/start"), struct{}{}, &task); e != nil {
			return lc.Result{}, e
		}
		if e = c.Wait(ctx, task); e != nil {
			return lc.Result{}, e
		}
	}

	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if e = c.Call(ctx, "POST", c.VM(m.VMID, "/agent/ping"), struct{}{}, nil); e == nil {
			break
		}
		select {
		case <-ctx.Done():
			return lc.Result{}, ctx.Err()
		case <-deadline.C:
			return lc.Result{}, fmt.Errorf("guest agent readiness timeout")
		case <-ticker.C:
		}
	}
	priv := base64.StdEncoding.EncodeToString([]byte(m.HostPrivateKey))
	pub := base64.StdEncoding.EncodeToString([]byte(m.HostPublicKey))
	hosts := base64.StdEncoding.EncodeToString([]byte(guest.Hosts(m)))
	script := "cloud-init status --wait >/dev/null; printf '%s' '" + hosts + "' | base64 -d > /etc/hosts; chmod 644 /etc/hosts; umask 077; printf '%s' '" + priv + "' | base64 -d > /etc/ssh/ssh_host_ed25519_key; printf '%s' '" + pub + "' | base64 -d > /etc/ssh/ssh_host_ed25519_key.pub; systemctl restart ssh"
	if e = c.GuestExec(ctx, m.VMID, []string{"/bin/sh", "-ec", script}); e != nil {
		return lc.Result{}, e
	}
	if e = remote.Wait(ctx, m.Address, "lab", cmd.ClientPrivateKey, m.HostPublicKey); e != nil {
		return lc.Result{}, e
	}
	return p.find(ctx, c, cmd)
}
func (c *Client) GuestExec(ctx context.Context, id int, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var pid struct {
		PID int `json:"pid"`
	}
	if e := c.Call(ctx, "POST", c.VM(id, "/agent/exec"), struct {
		Command []string `json:"command"`
	}{args}, &pid); e != nil {
		return e
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		var s struct {
			Exited int `json:"exited"`
			Code   int `json:"exitcode"`
		}
		if e := c.Call(ctx, "GET", c.VM(id, "/agent/exec-status?pid="+strconv.Itoa(pid.PID)), nil, &s); e != nil {
			return e
		}
		if s.Exited == 1 {
			if s.Code != 0 {
				return fmt.Errorf("guest operation failed with exit status %d", s.Code)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func (p *Provider) find(ctx context.Context, c *Client, cmd lc.Command) (lc.Result, error) {
	v, e := c.Config(ctx, cmd.Machine.VMID)
	if e != nil {
		return lc.Result{}, e
	}
	if e = checkOwner(v, cmd.Owner, cmd.Machine.Name); e != nil {
		return lc.Result{}, e
	}
	var status struct {
		Status string `json:"status"`
	}
	if e = c.Call(ctx, "GET", c.VM(cmd.Machine.VMID, "/status/current"), nil, &status); e != nil {
		return lc.Result{}, e
	}
	return lc.Result{Resource: lc.Resource{Kind: "machine", Name: cmd.Machine.Name, ID: strconv.Itoa(cmd.Machine.VMID), Owner: cmd.Owner, Address: cmd.Machine.Address, PrivateAddress: cmd.Machine.Address, Complete: status.Status == "running"}}, nil
}
func (p *Provider) destroy(ctx context.Context, c *Client, cmd lc.Command) (lc.Result, error) {
	r := cmd.Prior
	if r.Owner != cmd.Owner || cmd.Owner == "" {
		return lc.Result{}, fmt.Errorf("ownership_mismatch")
	}
	if r.Kind == "network" {
		return lc.Result{Absent: true}, nil
	}
	id, e := strconv.Atoi(r.ID)
	if e != nil {
		return lc.Result{}, e
	}
	var inventory []struct {
		VMID int `json:"vmid"`
	}
	if e = c.Call(ctx, "GET", "/nodes/"+url.PathEscape(c.Node)+"/qemu", nil, &inventory); e != nil {
		return lc.Result{}, e
	}
	found := false
	for _, v := range inventory {
		found = found || v.VMID == id
	}
	if !found {
		return lc.Result{Absent: true}, nil
	}
	v, e := c.Config(ctx, id)
	if e != nil {
		return lc.Result{}, e
	}
	if e = checkOwner(v, cmd.Owner, r.Name); e != nil {
		return lc.Result{}, e
	}
	if v.Template != 1 {
		var task string
		if e = c.Call(ctx, "POST", c.VM(id, "/status/stop"), struct{}{}, &task); e != nil {
			return lc.Result{}, e
		}
		if e = c.Wait(ctx, task); e != nil {
			return lc.Result{}, e
		}
	}
	v, e = c.Config(ctx, id)
	if e != nil {
		return lc.Result{}, e
	}
	if e = checkOwner(v, cmd.Owner, r.Name); e != nil {
		return lc.Result{}, e
	}
	var task string
	if e = c.Call(ctx, "DELETE", c.VM(id, "?purge=1&destroy-unreferenced-disks=1"), nil, &task); e != nil {
		return lc.Result{}, e
	}
	if e = c.Wait(ctx, task); e != nil {
		return lc.Result{}, e
	}
	if e = c.Call(ctx, "GET", "/nodes/"+url.PathEscape(c.Node)+"/qemu", nil, &inventory); e != nil {
		return lc.Result{}, e
	}
	for _, v := range inventory {
		if v.VMID == id {
			return lc.Result{}, fmt.Errorf("deletion is not yet confirmed")
		}
	}
	return lc.Result{Absent: true}, nil
}

func (p *Provider) FindBuild(ctx context.Context, cmd lc.Command) (lc.Resource, bool, error) {
	c, e := NewClient(cmd.Environment)
	if e != nil {
		return lc.Resource{}, false, e
	}
	var list []struct {
		ID int `json:"vmid"`
	}
	if e = c.Call(ctx, "GET", "/nodes/"+url.PathEscape(c.Node)+"/qemu", nil, &list); e != nil {
		return lc.Resource{}, false, e
	}
	found := false
	for _, v := range list {
		found = found || v.ID == cmd.Machine.VMID
	}
	if !found {
		return lc.Resource{}, false, nil
	}
	v, e := c.Config(ctx, cmd.Machine.VMID)
	if e != nil {
		return lc.Resource{}, false, e
	}
	if v.Template != 1 {
		if lc.Contains(strings.Split(v.Tags, ";"), cmd.Owner) && lc.Contains(strings.Split(v.Tags, ";"), "labcentral-v2") {
			return lc.Resource{}, false, nil
		}
		return lc.Resource{}, false, fmt.Errorf("ownership_mismatch: native build VM")
	}
	if e = checkOwner(v, cmd.Owner, cmd.Image.Name); e != nil {
		return lc.Resource{}, false, e
	}
	return lc.Resource{Kind: "image", Name: cmd.Image.Name, ID: strconv.Itoa(cmd.Machine.VMID), Owner: cmd.Owner}, true, nil
}

func (p *Provider) CleanupBuild(ctx context.Context, cmd lc.Command) error {
	c, e := NewClient(cmd.Environment)
	if e != nil {
		return e
	}
	var list []struct {
		ID int `json:"vmid"`
	}
	if e = c.Call(ctx, "GET", "/nodes/"+url.PathEscape(c.Node)+"/qemu", nil, &list); e != nil {
		return e
	}
	found := false
	for _, v := range list {
		found = found || v.ID == cmd.Machine.VMID
	}
	if !found {
		return nil
	}
	v, e := c.Config(ctx, cmd.Machine.VMID)
	if e != nil {
		return e
	}
	if !lc.Contains(strings.Split(v.Tags, ";"), cmd.Owner) || !lc.Contains(strings.Split(v.Tags, ";"), "labcentral-v2") {
		return fmt.Errorf("ownership_mismatch: temporary build VM")
	}
	if v.Template == 1 {
		return nil
	}
	if e = c.Call(ctx, "PUT", c.VM(cmd.Machine.VMID, "/config"), struct {
		Description string `json:"description"`
		Digest      string `json:"digest"`
	}{marker(cmd.Owner, cmd.Image.Name), v.Digest}, nil); e != nil {
		return e
	}
	_, e = p.destroy(ctx, c, lc.Command{Owner: cmd.Owner, Prior: lc.Resource{Kind: "machine", Name: cmd.Image.Name, ID: strconv.Itoa(cmd.Machine.VMID), Owner: cmd.Owner}})
	return e
}
