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
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/guest"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
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
		return nil, fmt.Errorf("Proxmox token environment variable %s is unset; export the setup token file secret", p.TokenEnv)
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
	return c.call(ctx, method, path, in, out, nil)
}
func (c *Client) call(ctx context.Context, method, path string, in, out any, changes *string) error {
	_, err := faultinject.Call(ctx, faultStep(method, path), func(ctx context.Context) (struct{}, error) {
		return struct{}{}, c.nativeCall(ctx, method, path, in, out, changes)
	})
	return faultError(err, method, path)
}
func faultStep(method, path string) string {
	path = strings.TrimPrefix(path, "/api2/json")
	path, _, _ = strings.Cut(path, "?")
	step := path
	if strings.HasPrefix(step, "/nodes/") {
		parts := strings.SplitN(strings.TrimPrefix(step, "/nodes/"), "/", 2)
		if len(parts) == 2 {
			step = "/" + parts[1]
		}
	}
	parts := strings.Split(step, "/")
	for i, part := range parts {
		if i > 0 && parts[i-1] == "storage" {
			parts[i] = "{storage}"
			continue
		}
		if i > 0 && parts[i-1] == "content" {
			parts = append(parts[:i], "{volume}")
			break
		}
		if i > 0 && parts[i-1] == "tasks" {
			parts[i] = "{task}"
			continue
		}
		if _, err := strconv.Atoi(part); err == nil {
			parts[i] = "{id}"
		}
	}
	return method + strings.Join(parts, "/")
}
func (c *Client) nativeCall(ctx context.Context, method, path string, in, out any, changes *string) error {
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
		Data    json.RawMessage `json:"data"`
		Changes string          `json:"changes"`
	}
	if e = json.Unmarshal(b, &envelope); e != nil {
		return e
	}
	if changes != nil {
		*changes = envelope.Changes
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
	hint := ""
	if e.Status == 401 {
		hint = "; verify token ID and secret environment variable"
	}
	if e.Status == 403 {
		hint = "; verify ACLs for both the user and privilege-separated token"
	}
	return fmt.Sprintf("Proxmox %s %s returned HTTP %d%s", e.Method, e.Path, e.Status, hint)
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
	SCSIController string            `json:"scsihw"`
	Lock           string            `json:"lock"`
	Agent          string            `json:"agent"`
	Scsi0          string            `json:"scsi0"`
	Ide2           string            `json:"ide2"`
	Tags           string            `json:"tags"`
	Description    string            `json:"description"`
	Digest         string            `json:"digest"`
	Template       int               `json:"template"`
	Name           string            `json:"name"`
	Net0           string            `json:"net0"`
	NICs           map[string]string `json:"-"`
	Disks          map[string]string `json:"-"`
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
	ctx = faultinject.WithOperation(ctx, "proxmox", cmd.Action)
	if cmd.Action == "check" || cmd.Action == "image.build" || cmd.Action == "network.create" || cmd.Action == "machine.create" || cmd.Action == "gateway.create" {
		if err := lc.CheckCapabilities(cmd.Source.Lab, cmd.Environment.Platform); err != nil {
			return lc.Result{}, err
		}
		if err := lc.CheckEgress(cmd.Source.Lab, cmd.Environment); err != nil {
			return lc.Result{}, err
		}
		if cmd.Machine.Name != "" {
			if err := lc.CheckCapabilities(lc.Lab{Machines: map[string]lc.Machine{cmd.Machine.Name: cmd.Machine.Spec}}, cmd.Environment.Platform); err != nil {
				return lc.Result{}, err
			}
		}
		if err := lc.CheckCommandPlatformOptions(cmd); err != nil {
			return lc.Result{}, err
		}
	}

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
		n, err := plannedNetwork(cmd)
		if err != nil {
			return lc.Result{}, err
		}
		b, _ := json.Marshal(n)
		return lc.Result{Resource: lc.Resource{Kind: "network", Name: "private", ID: strconv.Itoa(cmd.VLAN), Owner: cmd.Owner, Complete: true, Data: b}}, nil
	case "gateway.create":
		return p.gateway(ctx, c, cmd)
	case "machine.create":
		return p.machine(ctx, c, cmd)
	case "discover":
		if cmd.Machine.Name == "" {
			return lc.Result{Resource: lc.Resource{Kind: "network", Name: "private", ID: strconv.Itoa(cmd.VLAN), Owner: cmd.Owner}}, nil
		}
		if cmd.Prior.Kind == "gateway" {
			return p.findGateway(ctx, c, cmd)
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
	if err := lc.CheckCommandPlatformOptions(cmd); err != nil {
		return lc.Result{}, err
	}
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
	if err := checkLabBridge(ctx, c, cmd.Environment.Proxmox.LabBridge); err != nil {
		return lc.Result{}, err
	}
	if err := checkOperatorBridges(ctx, c, cmd.Environment); err != nil {
		return lc.Result{}, err
	}
	if err := checkStorage(ctx, c, cmd.Environment.Proxmox.Storage); err != nil {
		return lc.Result{}, err
	}
	if err := checkPermissions(ctx, c, cmd.Environment); err != nil {
		return lc.Result{}, err
	}
	for _, id := range []int{cmd.Environment.Proxmox.VMIDStart, cmd.Environment.Proxmox.VMIDEnd} {
		if err := requireVMAudit(ctx, c, id); err != nil {
			return lc.Result{}, err
		}
	}
	if err := checkRanges(ctx, c, cmd.Environment); err != nil {
		return lc.Result{}, err
	}
	var pool struct {
		PoolID string `json:"poolid"`
	}
	if err := c.Call(ctx, "GET", "/pools/"+url.PathEscape(cmd.Environment.Proxmox.Pool), nil, &pool); err != nil {
		return lc.Result{}, err
	}
	for name, base := range cmd.Environment.Bases {
		id, e := strconv.Atoi(base.ID)
		if e != nil {
			return lc.Result{}, fmt.Errorf("base %s id must be a template VMID: %w", name, e)
		}
		v, e := c.Config(ctx, id)
		if e != nil {
			return lc.Result{}, e
		}
		if v.Template != 1 || v.Digest != base.Identity {
			return lc.Result{}, fmt.Errorf("base %s template identity changed; verify the binding or rerun setup", name)
		}
		if !guestAgentEnabled(v.Agent) || v.Scsi0 == "" || v.Ide2 == "" {
			return lc.Result{}, fmt.Errorf("base needs scsi0, cloud-init drive and enabled guest agent; rerun environment setup")
		}
	}
	return lc.Result{Message: "node, privileges, storage, internal lab bridge and base templates verified; operator must reach DHCP/static gateway and build addresses on managementBridge/build.bridge"}, nil
}

type network struct {
	CIDR     string              `json:"cidr"`
	Gateway  string              `json:"gateway"`
	VLAN     int                 `json:"vlan"`
	Segments []lc.PlannedNetwork `json:"segments,omitempty"`
}

func (p *Provider) machine(ctx context.Context, c *Client, cmd lc.Command) (lc.Result, error) {
	if err := lc.CheckCommandPlatformOptions(cmd); err != nil {
		return lc.Result{}, err
	}
	management := []string{}
	if lc.InternetAccess(cmd.Source.Lab) {
		var err error
		management, err = managementCIDRs(ctx, cmd.Environment)
		if err != nil {
			return lc.Result{}, err
		}
	}
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
			return 0, cloneError(e)
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
		if v.Lock != "" {
			return 0, false, fmt.Errorf("clone is still locked; retry recovery")
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
	if v.Lock != "" {
		return lc.Result{}, fmt.Errorf("clone is still locked; retry recovery")
	}
	ip := "ip=" + m.Address + "/" + strings.Split(net.CIDR, "/")[1]
	if net.Gateway != "" && lc.InternetAccess(cmd.Source.Lab) {
		ip += ",gw=" + net.Gateway
	}

	cfg := machineConfig(cmd, v, ip)

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
	opts := struct {
		Enable int    `json:"enable"`
		In     string `json:"policy_in"`
		Out    string `json:"policy_out"`
	}{1, "DROP", "DROP"}
	if e = c.Call(ctx, "PUT", c.VM(m.VMID, "/firewall/options"), opts, nil); e != nil {
		return lc.Result{}, e
	}
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

	desired := firewallRules(cmd.Environment, net.CIDR, lc.InternetAccess(cmd.Source.Lab), management...)
	if len(m.NICs) > 0 {
		desired = machineFirewallRules(cmd.Environment, m, net.Segments, management...)
	}
	if e = installFirewallRules(ctx, c, m.VMID, desired); e != nil {
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
	script := cloudInitWait + guest.ProxmoxLoginPolicy + "printf '%s' '" + hosts + "' | base64 -d > /etc/hosts; chmod 644 /etc/hosts; " + guest.ProxmoxHostnameAlias + "umask 077; printf '%s' '" + priv + "' | base64 -d > /etc/ssh/ssh_host_ed25519_key; printf '%s' '" + pub + "' | base64 -d > /etc/ssh/ssh_host_ed25519_key.pub; systemctl restart ssh"
	if e = c.GuestExec(ctx, m.VMID, []string{"/bin/sh", "-ec", script}); e != nil {
		return lc.Result{}, e
	}
	if len(m.NICs) > 0 {
		current, err := c.Config(ctx, m.VMID)
		if err != nil {
			return lc.Result{}, err
		}
		macs, err := current.MACs(len(m.NICs))
		if err != nil {
			return lc.Result{}, err
		}
		script, err := guest.NetworkExec(m, macs...)
		if err != nil {
			return lc.Result{}, err
		}
		if e = c.GuestExec(ctx, m.VMID, []string{"/bin/sh", "-ec", script}); e != nil {
			return lc.Result{}, e
		}
	}
	dialer, e := lc.MachineDialer(cmd.Gateway, cmd.ClientPrivateKey)
	if e != nil {
		return lc.Result{}, e
	}
	defer dialer.Close()
	if e = remote.WaitWithDialer(ctx, dialer, m.Address, "lab", cmd.ClientPrivateKey, m.HostPublicKey); e != nil {
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
	if err := requireVMAudit(ctx, c, id); err != nil {
		return lc.Result{}, err
	}
	var inventory []struct {
		VMID int    `json:"vmid"`
		Node string `json:"node"`
	}
	if e = c.Call(ctx, "GET", "/cluster/resources?type=vm", nil, &inventory); e != nil {
		return lc.Result{}, e
	}
	found := false
	for _, v := range inventory {
		if v.VMID == id {
			if v.Node != c.Node {
				return lc.Result{}, fmt.Errorf("recovery_required: VM is outside the configured node; investigate ownership before cleanup")
			}
			found = true
		}
	}

	if !found {
		if err := requireVMAudit(ctx, c, id); err != nil {
			return lc.Result{}, err
		}
		err := p.cleanupDisks(ctx, c, cmd, id)
		return lc.Result{Absent: err == nil}, err
	}
	v, e := c.Config(ctx, id)
	if e != nil {
		return lc.Result{}, e
	}
	if e = checkOwner(v, cmd.Owner, r.Name); e != nil {
		return lc.Result{}, e
	}
	if v.Lock != "" {
		return lc.Result{}, fmt.Errorf("recovery_required: VM mutation is still locked; retry cleanup after the native task finishes")
	}
	if e = p.rememberDisks(ctx, c, cmd, id, v); e != nil {
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
	if v.Lock != "" {
		return lc.Result{}, fmt.Errorf("recovery_required: VM mutation is still locked; retry cleanup after the native task finishes")
	}
	if e = p.rememberDisks(ctx, c, cmd, id, v); e != nil {
		return lc.Result{}, e
	}
	var task string
	if e = c.Call(ctx, "DELETE", c.VM(id, ""), nil, &task); e != nil {
		return lc.Result{}, e
	}
	if e = c.Wait(ctx, task); e != nil {
		return lc.Result{}, e
	}
	if e = c.Call(ctx, "GET", "/cluster/resources?type=vm", nil, &inventory); e != nil {
		return lc.Result{}, e
	}
	for _, v := range inventory {
		if v.VMID == id {
			return lc.Result{}, fmt.Errorf("deletion is not yet confirmed")
		}
	}
	e = p.cleanupDisks(ctx, c, cmd, id)
	return lc.Result{Absent: e == nil}, e
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
		if err := requireVMAudit(ctx, c, cmd.Machine.VMID); err != nil {
			return lc.Resource{}, false, err
		}
		return lc.Resource{}, false, nil
	}
	v, e := c.Config(ctx, cmd.Machine.VMID)
	if e != nil {
		return lc.Resource{}, false, e
	}
	if e = p.checkBuildOwner(v, cmd); e != nil {
		return lc.Resource{}, false, e
	}
	if v.Lock != "" {
		return lc.Resource{}, false, fmt.Errorf("recovery_required: build mutation still locked; retry recovery")
	}
	if v.Template != 1 {
		return lc.Resource{}, false, nil
	}
	return lc.Resource{Kind: "image", Name: cmd.Image.Name, ID: strconv.Itoa(cmd.Machine.VMID), Owner: cmd.Owner}, true, nil
}

func (p *Provider) CleanupBuild(ctx context.Context, cmd lc.Command) error {
	c, e := NewClient(cmd.Environment)
	if e != nil {
		return e
	}
	if e = p.reconcileBuildMutations(ctx, c, cmd); e != nil {
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
		if err := requireVMAudit(ctx, c, cmd.Machine.VMID); err != nil {
			return err
		}
		return p.cleanupDisks(ctx, c, cmd, cmd.Machine.VMID)
	}
	v, e := c.Config(ctx, cmd.Machine.VMID)
	if e != nil {
		return e
	}
	if e = p.checkBuildOwner(v, cmd); e != nil {
		return e
	}
	if v.Lock != "" {
		return fmt.Errorf("recovery_required: build mutation still locked; retry cleanup")
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
	_, e = p.destroy(ctx, c, lc.Command{Owner: cmd.Owner, Environment: cmd.Environment, Prior: lc.Resource{Kind: "machine", Name: cmd.Image.Name, ID: strconv.Itoa(cmd.Machine.VMID), Owner: cmd.Owner}})
	return e
}

// Clone firmware and disk controllers remain properties of the base image.
func machineConfig(cmd lc.Command, v VMConfig, ip string) map[string]any {
	m := cmd.Machine
	model := "virtio"
	options := m.Spec.Platform.Proxmox
	if options != nil && options.NICModel != "" {
		model = options.NICModel
	}
	// Replace only the model, preserving the cloned NIC's MAC for every model.
	nic := model
	if _, mac, ok := strings.Cut(strings.Split(v.Net0, ",")[0], "="); ok && mac != "" {
		nic += "=" + mac
	}
	cfg := map[string]any{
		"cores": m.CPU, "memory": m.MemoryMiB, "ciupgrade": 0,
		"net0":      fmt.Sprintf("%s,bridge=%s,tag=%d,firewall=1", nic, cmd.Environment.Proxmox.LabBridge, cmd.VLAN),
		"ipconfig0": ip, "ciuser": "lab", "sshkeys": strings.ReplaceAll(url.QueryEscape(m.PublicKey), "+", "%20"), "agent": "enabled=1", "digest": v.Digest,
	}
	if len(m.NICs) > 0 {
		var removed []string
		for key := range v.NICs {
			index, err := strconv.Atoi(strings.TrimPrefix(key, "net"))
			if err == nil && index >= len(m.NICs) {
				removed = append(removed, key, "ipconfig"+strconv.Itoa(index))
			}
		}
		if len(removed) > 0 {
			sort.Strings(removed)
			cfg["delete"] = strings.Join(removed, ",")
		}
		for j, planned := range m.NICs {
			modelNIC := model
			original := v.NICs[fmt.Sprintf("net%d", j)]
			if j == 0 {
				original = v.Net0
			}
			if _, mac, ok := strings.Cut(strings.Split(original, ",")[0], "="); ok && mac != "" {
				modelNIC += "=" + mac
			}
			cfg[fmt.Sprintf("net%d", j)] = fmt.Sprintf("%s,bridge=%s,tag=%d,firewall=1", modelNIC, cmd.Environment.Proxmox.LabBridge, planned.VLAN)
			ipconfig := "ip=" + planned.Address + "/" + strings.Split(planned.CIDR, "/")[1]
			if planned.Default && planned.Gateway != "" {
				ipconfig += ",gw=" + planned.Gateway
			}
			cfg[fmt.Sprintf("ipconfig%d", j)] = ipconfig
		}
	}
	if lc.Contains(m.Spec.Access, "desktop") {
		cfg["vga"] = "virtio"
	}

	if options != nil {
		if options.CPUType != "" {
			cfg["cpu"] = options.CPUType
		}
		if options.MemoryBalloon != nil {
			cfg["balloon"] = 0
			if *options.MemoryBalloon {
				cfg["balloon"] = m.MemoryMiB
			}
		}
	}
	return cfg
}
