package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/OutboundSpade/LabCentral/v2/internal/privatefile"
)

func (s *Setup) readRole() string { return "LabCentralReadV2" + s.state.Plan.Owner[:12] }
func (s *Setup) role() string     { return "LabCentralV2" + s.state.Plan.Owner[:12] }
func setupPrivileges() []string {
	return append(append([]string{}, runtimePrivileges...), "Datastore.Audit", "Datastore.AllocateSpace", "Sys.Audit", "SDN.Use", "Pool.Audit")
}
func (s *Setup) privileges() string {
	return strings.Join(s.state.Plan.RolePrivileges, " ")
}

func (s *Setup) verifyPool(ctx context.Context) error {
	var v struct {
		Comment string `json:"comment"`
	}
	if err := s.Client.Call(ctx, "GET", "/pools/"+url.PathEscape(s.state.Plan.Environment.Proxmox.Pool), nil, &v); err != nil {
		return err
	}
	if v.Comment != s.ownedComment() {
		return fmt.Errorf("ownership_mismatch: pool")
	}
	return nil
}
func (s *Setup) verifyUser(ctx context.Context) error {
	var v struct {
		Comment string `json:"comment"`
	}
	if err := s.Client.Call(ctx, "GET", "/access/users/labcentral@pve", nil, &v); err != nil {
		return err
	}
	if v.Comment != s.ownedComment()+":role="+s.role() {
		return fmt.Errorf("ownership_mismatch: user or role binding")
	}
	return nil
}
func (s *Setup) verifyRole(ctx context.Context) error {
	if err := s.verifyUser(ctx); err != nil {
		return err
	}
	var v map[string]any
	if err := s.Client.Call(ctx, "GET", "/access/roles/"+s.role(), nil, &v); err != nil {
		return err
	}
	actual := []string{}
	for k, value := range v {
		if n, ok := value.(float64); ok && n == 1 {
			actual = append(actual, k)
		}
	}
	expected := strings.Fields(s.privileges())
	sort.Strings(actual)
	sort.Strings(expected)
	if strings.Join(actual, " ") != strings.Join(expected, " ") {
		return fmt.Errorf("ownership_mismatch: role privileges changed")
	}
	return nil
}
func (s *Setup) verifyToken(ctx context.Context) error {
	var v struct {
		Comment string `json:"comment"`
		Privsep int    `json:"privsep"`
	}
	if err := s.Client.Call(ctx, "GET", "/access/users/labcentral@pve/token/labs", nil, &v); err != nil {
		return err
	}
	if v.Comment != s.ownedComment() || v.Privsep != 1 {
		return fmt.Errorf("ownership_mismatch: token")
	}
	if _, err := os.Stat(s.File + ".token"); err != nil {
		return fmt.Errorf("token secret was not saved; use teardown then setup to recreate safely")
	}
	return nil
}
func (s *Setup) configureResources(ctx context.Context) error {
	c, p := s.Client, s.state.Plan.Environment.Proxmox
	if s.state.Steps["pool"] == "" {
		var pools []struct {
			ID string `json:"poolid"`
		}
		if err := c.Call(ctx, "GET", "/pools", nil, &pools); err != nil {
			return err
		}
		for _, v := range pools {
			if v.ID == p.Pool {
				return fmt.Errorf("refusing existing pool without ownership")
			}
		}
	}
	if err := s.step(ctx, "pool", func() error {
		return c.Call(ctx, "POST", "/pools", map[string]any{"poolid": p.Pool, "comment": s.ownedComment()}, nil)
	}, func() error { return s.verifyPool(ctx) }); err != nil {
		return err
	}
	if s.state.Steps["user"] == "" {
		var users []struct {
			ID string `json:"userid"`
		}
		if err := c.Call(ctx, "GET", "/access/users", nil, &users); err != nil {
			return err
		}
		for _, v := range users {
			if v.ID == "labcentral@pve" {
				return fmt.Errorf("refusing existing user without ownership")
			}
		}
	}
	if err := s.step(ctx, "user", func() error {
		return c.Call(ctx, "POST", "/access/users", map[string]any{"userid": "labcentral@pve", "comment": s.ownedComment() + ":role=" + s.role(), "enable": 1}, nil)
	}, func() error { return s.verifyUser(ctx) }); err != nil {
		return err
	}
	if err := s.step(ctx, "role", func() error {
		return c.Call(ctx, "POST", "/access/roles", map[string]any{"roleid": s.role(), "privs": s.privileges()}, nil)
	}, func() error { return s.verifyRole(ctx) }); err != nil {
		return err
	}
	if err := s.step(ctx, "token", func() error {
		var token struct {
			Value string `json:"value"`
		}
		if err := c.Call(ctx, "POST", "/access/users/labcentral@pve/token/labs", map[string]any{"privsep": 1, "comment": s.ownedComment()}, &token); err != nil {
			return err
		}
		if token.Value == "" {
			return fmt.Errorf("API returned no token secret")
		}
		return privatefile.Write(s.File+".token", []byte(token.Value+"\n"))
	}, func() error { return s.verifyToken(ctx) }); err != nil {
		return err
	}
	// Both ACLs are necessary for privilege-separated tokens. Only exact owned
	// identities and role IDs are updated; no administrator permissions are granted.
	if err := s.ensureACLs(ctx); err != nil {
		return err
	}

	if s.state.Plan.StorageContentBefore != s.state.Plan.StorageContentAfter {
		if err := s.step(ctx, "storage", func() error {
			return c.Call(ctx, "PUT", "/storage/"+url.PathEscape(p.Storage), map[string]any{"content": s.state.Plan.StorageContentAfter}, nil)
		}, func() error {
			var st struct {
				Content string `json:"content"`
			}
			if err := c.Call(ctx, "GET", "/storage/"+url.PathEscape(p.Storage), nil, &st); err != nil {
				return err
			}
			if !sameContent(st.Content, s.state.Plan.StorageContentAfter) {
				return fmt.Errorf("storage content changed")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if err := s.step(ctx, "bridge", func() error {
		if err := s.noPendingNetwork(ctx); err != nil {
			return err
		}
		return c.Call(ctx, "POST", c.nodePath("/network"), map[string]any{"iface": p.LabBridge, "type": "bridge", "autostart": 1, "bridge_vlan_aware": 1, "bridge_ports": "", "comments": s.ownedComment()}, nil)
	}, func() error { return s.verifyBridge(ctx) }); err != nil {
		return err
	}
	return s.applyNetwork(ctx, false)
}
func sameContent(a, b string) bool {
	x, y := strings.Split(a, ","), strings.Split(b, ",")
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}
func (s *Setup) verifyBridge(ctx context.Context) error {
	var v bridge
	if err := s.Client.Call(ctx, "GET", s.Client.nodePath("/network/"+url.PathEscape(s.state.Plan.Environment.Proxmox.LabBridge)), nil, &v); err != nil {
		return err
	}
	if strings.TrimSpace(v.Comments) != s.ownedComment() {
		return fmt.Errorf("ownership_mismatch: bridge")
	}
	return checkBridgeDefinition(ctx, s.Client, s.state.Plan.Environment.Proxmox.LabBridge, true)
}
func (s *Setup) applyNetwork(ctx context.Context, remove bool) error {
	var changes string
	if err := s.Client.call(ctx, "GET", s.Client.nodePath("/network"), nil, nil, &changes); err != nil {
		return err
	}
	if strings.TrimSpace(changes) == "" {
		return nil
	}
	if err := s.checkNetworkDiff(changes, remove); err != nil {
		return err
	}
	var task string
	if err := s.Client.Call(ctx, "PUT", s.Client.nodePath("/network"), map[string]any{}, &task); err != nil {
		return err
	}
	return s.Client.Wait(ctx, task)
}
func (s *Setup) checkNetworkDiff(diff string, remove bool) error {
	name := s.state.Plan.Environment.Proxmox.LabBridge
	allowed := map[string]bool{"": true, "#" + s.ownedComment(): true, "# " + s.ownedComment(): true, "auto " + name: true, "iface " + name + " inet manual": true, "bridge-ports none": true, "bridge-stp off": true, "bridge-fd 0": true, "bridge-vlan-aware yes": true, "bridge-vids 2-4094": true}
	changed := false
	inOwnedBlock, interfaces := false, 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || line == "" {
			continue
		}
		if line[0] != '+' && line[0] != '-' {
			if strings.TrimSpace(line) != "" {
				inOwnedBlock = false
			}
			continue
		}
		content := strings.TrimSpace(line[1:])
		if content == "" {
			continue
		}
		if remove && line[0] != '-' || !remove && line[0] != '+' || !allowed[content] {
			return fmt.Errorf("unrelated pending network changes; refusing network apply")
		}
		if content == "auto "+name {
			inOwnedBlock = true
		}
		if content == "iface "+name+" inet manual" {
			inOwnedBlock = true
			interfaces++
		}
		if !inOwnedBlock {
			return fmt.Errorf("pending network change is outside the owned bridge; refusing network apply")
		}
		changed = true
	}
	if !changed || interfaces != 1 {
		return fmt.Errorf("cannot verify pending bridge change; refusing network apply")
	}
	return nil
}

func (s *Setup) aclPaths() []string {
	p := s.state.Plan.Environment.Proxmox
	paths := []string{"/pool/" + p.Pool, "/storage/" + p.Storage, "/nodes/" + p.Node, "/sdn/zones/localnetwork/" + p.LabBridge, "/sdn/zones/localnetwork/" + p.ManagementBridge}
	if s.state.Plan.Environment.Build.Bridge != p.ManagementBridge {
		paths = append(paths, "/sdn/zones/localnetwork/"+s.state.Plan.Environment.Build.Bridge)
	}
	return paths
}

// A separate read-only VM inventory grant survives VM-specific ACL cleanup.
// Mutating permissions remain scoped to the owned pool and target storage.
func (s *Setup) ensureACLs(ctx context.Context) error {
	c, p := s.Client, s.state.Plan
	if err := s.verifyPool(ctx); err != nil {
		return err
	}
	var pool struct {
		Members []struct {
			ID int `json:"vmid"`
		} `json:"members"`
	}
	if err := c.Call(ctx, "GET", "/pools/"+url.PathEscape(p.Environment.Proxmox.Pool), nil, &pool); err != nil {
		return err
	}
	owned := map[int]bool{}
	for _, m := range pool.Members {
		owned[m.ID] = true
	}
	var inventory []struct {
		ID int `json:"vmid"`
	}
	if err := c.Call(ctx, "GET", "/cluster/resources?type=vm", nil, &inventory); err != nil {
		return err
	}
	for _, v := range inventory {
		if (v.ID >= p.Environment.Proxmox.VMIDStart && v.ID <= p.Environment.Proxmox.VMIDEnd || v.ID == p.BaseVMID) && !owned[v.ID] {
			return fmt.Errorf("ownership_mismatch: reserved VMID is occupied outside the owned pool")
		}
	}
	var acls []struct {
		Path string `json:"path"`
		ID   string `json:"ugid"`
		Role string `json:"roleid"`
	}
	if err := c.Call(ctx, "GET", "/access/acl", nil, &acls); err != nil {
		return err
	}
	if err := s.step(ctx, "read-role", func() error {
		return c.Call(ctx, "POST", "/access/roles", map[string]any{"roleid": s.readRole(), "privs": "VM.Audit"}, nil)
	}, func() error { return s.verifyReadRole(ctx) }); err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, acl := range acls {
		if acl.ID == "labcentral@pve" || acl.ID == "labcentral@pve!labs" {
			if acl.Role != s.role() && acl.Role != s.readRole() {
				return fmt.Errorf("ownership_mismatch: runtime identity has unrelated ACLs")
			}
			existing[acl.Path+"|"+acl.ID+"|"+acl.Role] = true
		}
	}
	grant := func(path, role string) error {
		for _, kind := range []string{"users", "tokens"} {
			id := "labcentral@pve"
			if kind == "tokens" {
				id += "!labs"
			}
			if existing[path+"|"+id+"|"+role] {
				continue
			}
			if err := c.Call(ctx, "PUT", "/access/acl", map[string]any{"path": path, "roles": role, kind: id, "propagate": 1}, nil); err != nil {
				return err
			}
		}
		return nil
	}
	if err := grant("/vms", s.readRole()); err != nil {
		return err
	}
	for _, path := range s.aclPaths() {
		if err := grant(path, s.role()); err != nil {
			return err
		}
	}
	return nil
}
func (s *Setup) verifyReadRole(ctx context.Context) error {
	if err := s.verifyUser(ctx); err != nil {
		return err
	}
	var privileges map[string]int
	if err := s.Client.Call(ctx, "GET", "/access/roles/"+s.readRole(), nil, &privileges); err != nil {
		return err
	}
	if len(privileges) != 1 || privileges["VM.Audit"] != 1 {
		return fmt.Errorf("ownership_mismatch: read-only inventory role changed")
	}
	return nil
}
