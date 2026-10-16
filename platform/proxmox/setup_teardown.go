package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/store"
)

// Teardown removes the exact setup inventory, after checking remote ownership.
// Unknown deletions retain the local receipt so reruns can confirm absence.
func (s *Setup) Teardown(ctx context.Context) error {
	if s.state.Plan.Owner == "" {
		return fmt.Errorf("no setup ownership receipt for this environment")
	}
	st, err := store.Open(s.File + ".setup-state")
	if err != nil {
		return err
	}
	defer st.Close()
	unlock, err := st.LockKey("setup")
	if err != nil {
		return err
	}
	defer unlock()
	if err = s.reload(); err != nil {
		return err
	}
	if s.state.Removed {
		return nil
	}
	c, p := s.Client, s.state.Plan
	if err = s.preflightIdentityTeardown(ctx); err != nil {
		return err
	}
	c.Node = p.Environment.Proxmox.Node
	// Never take down networking or access while other guests still use the pool,
	// including guests that an operator added after setup.
	var pools []struct {
		ID string `json:"poolid"`
	}
	if err = c.Call(ctx, "GET", "/pools", nil, &pools); err != nil {
		return err
	}
	poolExists := false
	for _, v := range pools {
		poolExists = poolExists || v.ID == p.Environment.Proxmox.Pool
	}
	if poolExists {
		if err = s.verifyPool(ctx); err != nil {
			return err
		}
		var pool struct {
			Members []struct {
				ID   int    `json:"vmid"`
				Type string `json:"type"`
			} `json:"members"`
		}
		if err = c.Call(ctx, "GET", "/pools/"+url.PathEscape(p.Environment.Proxmox.Pool), nil, &pool); err != nil {
			return err
		}
		for _, m := range pool.Members {
			if m.Type == "qemu" && m.ID != p.BaseVMID || m.Type == "lxc" {
				return fmt.Errorf("pool still contains guests; destroy labs and remove built images before teardown")
			}
		}
	}
	var resources []struct {
		ID   int    `json:"vmid"`
		Node string `json:"node"`
		Type string `json:"type"`
	}
	if err = c.Call(ctx, "GET", "/cluster/resources?type=vm", nil, &resources); err != nil {
		return err
	}
	for _, r := range resources {
		if r.Node != c.Node {
			continue
		}
		if r.Type == "lxc" {
			var config map[string]any
			if err = c.Call(ctx, "GET", c.nodePath("/lxc/"+strconv.Itoa(r.ID)+"/config"), nil, &config); err != nil {
				return err
			}
			for key, value := range config {
				nic, _ := value.(string)
				if strings.HasPrefix(key, "net") {
					for _, part := range strings.Split(nic, ",") {
						if part == "bridge="+p.Environment.Proxmox.LabBridge {
							return fmt.Errorf("lab bridge is still used by a container; refusing teardown")
						}
					}
				}
			}
			continue
		}
		if r.Type != "qemu" {
			continue
		}
		if r.ID == p.BaseVMID {
			if err = s.deleteStep(ctx, "base", func() error {
				_, err := (&Provider{}).destroy(ctx, c, lc.Command{Owner: p.Owner, Prior: lc.Resource{Kind: "machine", Name: "base", ID: strconv.Itoa(p.BaseVMID), Owner: p.Owner}})
				return err
			}); err != nil {
				return err
			}
			continue
		}
		var config map[string]any
		if err = c.Call(ctx, "GET", c.VM(r.ID, "/config"), nil, &config); err != nil {
			return err
		}
		for key, value := range config {
			nic, _ := value.(string)
			if strings.HasPrefix(key, "net") {
				for _, part := range strings.Split(nic, ",") {
					if part == "bridge="+p.Environment.Proxmox.LabBridge {
						return fmt.Errorf("lab bridge is still used by a guest; refusing teardown")
					}
				}
			}
		}
	}
	for _, v := range []struct{ Key, ID string }{{"seed", p.Environment.Proxmox.Storage + ":iso/" + p.SeedName}, {"image", p.Environment.Proxmox.Storage + ":import/" + p.ImageName}} {
		if err = s.deleteStep(ctx, v.Key, func() error {
			if _, found, err := s.findVolume(ctx, v.ID); err != nil {
				return err
			} else if !found {
				return nil
			}
			if err := s.verifyVolume(ctx, v.Key+"-volume", v.ID); err != nil {
				return err
			}
			var task string
			if err := c.Call(ctx, "DELETE", c.nodePath("/storage/"+url.PathEscape(p.Environment.Proxmox.Storage)+"/content/"+url.PathEscape(v.ID)), nil, &task); err != nil {
				return err
			}
			if task != "" {
				if err := c.Wait(ctx, task); err != nil {
					return err
				}
			}
			if _, found, err := s.findVolume(ctx, v.ID); err != nil {
				return err
			} else if found {
				return fmt.Errorf("setup volume deletion not confirmed")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if err = s.deleteStep(ctx, "bridge", func() error {
		var bridges []bridge
		if err := c.Call(ctx, "GET", c.nodePath("/network"), nil, &bridges); err != nil {
			return err
		}
		exists := false
		for _, b := range bridges {
			exists = exists || b.Interface == p.Environment.Proxmox.LabBridge
		}
		if exists {
			if err := s.verifyBridge(ctx); err != nil {
				return err
			}
			var changes string
			if err := c.call(ctx, "GET", c.nodePath("/network"), nil, nil, &changes); err != nil {
				return err
			}
			if strings.TrimSpace(changes) != "" {
				if err := s.checkNetworkDiff(changes, false); err != nil {
					return err
				}
			}

			if err := c.Call(ctx, "DELETE", c.nodePath("/network/"+url.PathEscape(p.Environment.Proxmox.LabBridge)), nil, nil); err != nil {
				return err
			}
		}
		return s.applyNetwork(ctx, true)
	}); err != nil {
		return err
	}
	if p.StorageContentBefore != p.StorageContentAfter {
		if err = s.deleteStep(ctx, "storage", func() error {
			var current struct {
				Content string `json:"content"`
			}
			path := "/storage/" + url.PathEscape(p.Environment.Proxmox.Storage)
			if err := c.Call(ctx, "GET", path, nil, &current); err != nil {
				return err
			}
			if sameContent(current.Content, p.StorageContentBefore) {
				return nil
			}
			if !sameContent(current.Content, p.StorageContentAfter) {
				return fmt.Errorf("storage content changed outside setup; refusing to restore")
			}
			var volumes []volume
			if err := c.Call(ctx, "GET", c.nodePath("/storage/"+url.PathEscape(p.Environment.Proxmox.Storage)+"/content"), nil, &volumes); err != nil {
				return err
			}
			for _, v := range volumes {
				if !lc.Contains(strings.Split(p.StorageContentBefore, ","), v.Content) {
					return fmt.Errorf("storage has new content; refusing to disable its type")
				}
			}
			return c.Call(ctx, "PUT", path, map[string]any{"content": p.StorageContentBefore}, nil)
		}); err != nil {
			return err
		}
	}
	// The user comment binds the otherwise unannotatable role to this setup.
	// Check both before removing ACLs; never delete a role based on its name.
	if s.state.Steps["delete-role"] != "done" && s.state.Steps["role"] != "" {
		var roles []struct {
			ID string `json:"roleid"`
		}
		if err = c.Call(ctx, "GET", "/access/roles", nil, &roles); err != nil {
			return err
		}
		for _, r := range roles {
			if r.ID == s.role() {
				if err = s.verifyRole(ctx); err != nil {
					return err
				}
			}
		}
	}

	if err = s.deleteStep(ctx, "acl", func() error {
		var acls []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			UGID string `json:"ugid"`
			Role string `json:"roleid"`
		}
		if err := c.Call(ctx, "GET", "/access/acl", nil, &acls); err != nil {
			return err
		}
		for _, a := range acls {
			if a.Role != s.role() && a.Role != s.readRole() {
				continue
			}
			if a.UGID != "labcentral@pve" && a.UGID != "labcentral@pve!labs" {
				return fmt.Errorf("role has unrelated ACLs; refusing teardown")
			}
			identity := "users"
			if a.Type == "token" {
				identity = "tokens"
			}
			if err := c.Call(ctx, "PUT", "/access/acl", map[string]any{"path": a.Path, "roles": a.Role, identity: a.UGID, "delete": 1}, nil); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err = s.deleteStep(ctx, "read-role", func() error {
		if s.state.Steps["read-role"] == "" {
			return nil
		}
		var roles []struct {
			ID string `json:"roleid"`
		}
		if err := c.Call(ctx, "GET", "/access/roles", nil, &roles); err != nil {
			return err
		}
		exists := false
		for _, role := range roles {
			exists = exists || role.ID == s.readRole()
		}
		if !exists {
			return nil
		}
		if err := s.verifyReadRole(ctx); err != nil {
			return err
		}
		return c.Call(ctx, "DELETE", "/access/roles/"+s.readRole(), nil, nil)
	}); err != nil {
		return err
	}

	if err = s.deleteStep(ctx, "role", func() error {
		if s.state.Steps["role"] == "" {
			return nil
		}
		// An interrupted delete is reconciled by the authoritative role inventory.
		var roles []struct {
			ID string `json:"roleid"`
		}
		if err := c.Call(ctx, "GET", "/access/roles", nil, &roles); err != nil {
			return err
		}
		found := false
		for _, r := range roles {
			found = found || r.ID == s.role()
		}
		if !found {
			return nil
		}
		if err := s.verifyRole(ctx); err != nil {
			return err
		}
		return c.Call(ctx, "DELETE", "/access/roles/"+s.role(), nil, nil)
	}); err != nil {
		return err
	}
	if err = s.deleteStep(ctx, "user", func() error {
		var users []struct {
			ID string `json:"userid"`
		}
		if err := c.Call(ctx, "GET", "/access/users", nil, &users); err != nil {
			return err
		}
		found := false
		for _, u := range users {
			found = found || u.ID == "labcentral@pve"
		}
		if !found {
			return nil
		}
		if err := s.verifyUser(ctx); err != nil {
			return err
		}
		// Deleting the exact owned user removes its privilege-separated API token.
		return c.Call(ctx, "DELETE", "/access/users/labcentral@pve", nil, nil)
	}); err != nil {
		return err
	}
	if poolExists {
		if err = s.deleteStep(ctx, "pool", func() error {
			if err := s.verifyPool(ctx); err != nil {
				return err
			}
			return c.Call(ctx, "DELETE", "/pools/"+url.PathEscape(p.Environment.Proxmox.Pool), nil, nil)
		}); err != nil {
			return err
		}
	}
	for _, file := range []string{s.File, s.File + ".token"} {
		if err = os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	s.state.Removed = true
	return s.save()
}
func (s *Setup) deleteStep(ctx context.Context, key string, remove func() error) error {
	key = "delete-" + key
	if s.state.Steps[key] == "done" {
		return nil
	}
	s.state.Steps[key] = "intent"
	if err := s.save(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := remove(); err != nil {
		return err
	}
	s.state.Steps[key] = "done"
	return s.save()
}

func (s *Setup) preflightIdentityTeardown(ctx context.Context) error {
	c := s.Client
	var users []struct {
		ID string `json:"userid"`
	}
	if err := c.Call(ctx, "GET", "/access/users", nil, &users); err != nil {
		return err
	}
	exists := false
	for _, u := range users {
		exists = exists || u.ID == "labcentral@pve"
	}
	if exists {
		if err := s.verifyUser(ctx); err != nil {
			return err
		}
		var roles []struct {
			ID string `json:"roleid"`
		}
		if err := c.Call(ctx, "GET", "/access/roles", nil, &roles); err != nil {
			return err
		}
		for _, role := range roles {
			if role.ID == s.role() {
				if err := s.verifyRole(ctx); err != nil {
					return err
				}
			}
			if role.ID == s.readRole() {
				if err := s.verifyReadRole(ctx); err != nil {
					return err
				}
			}
		}
		var tokens []struct {
			ID      string `json:"tokenid"`
			Comment string `json:"comment"`
			Privsep int    `json:"privsep"`
		}
		if err := c.Call(ctx, "GET", "/access/users/labcentral@pve/token", nil, &tokens); err != nil {
			return err
		}
		for _, token := range tokens {
			if token.ID != "labs" || token.Comment != s.ownedComment() || token.Privsep != 1 {
				return fmt.Errorf("ownership_mismatch: user has unrelated or modified API tokens; refusing teardown")
			}
		}
	}
	paths := map[string]bool{}
	for _, path := range s.aclPaths() {
		paths[path] = true
	}
	var acls []struct {
		Path string `json:"path"`
		ID   string `json:"ugid"`
		Role string `json:"roleid"`
	}
	if err := c.Call(ctx, "GET", "/access/acl", nil, &acls); err != nil {
		return err
	}
	for _, acl := range acls {
		ownIdentity := acl.ID == "labcentral@pve" || acl.ID == "labcentral@pve!labs"
		allowed := acl.Role == s.role() && paths[acl.Path] || acl.Role == s.readRole() && acl.Path == "/vms"
		if ownIdentity && !allowed || (acl.Role == s.role() || acl.Role == s.readRole()) && !ownIdentity {
			return fmt.Errorf("ownership_mismatch: identity has unrelated ACLs; refusing teardown")
		}
	}

	return nil
}
