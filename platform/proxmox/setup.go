package proxmox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/privatefile"
	"github.com/OutboundSpade/LabCentral/v2/internal/store"
	"go.yaml.in/yaml/v3"
)

// SetupOptions carries credential references only. The admin secret is resolved
// by NewClient and is never persisted in setup state or the environment.
type SetupOptions struct {
	URL, AdminTokenID, AdminTokenEnv, CAFile               string
	Node, Storage, ManagementBridge, LabBridge, Pool, CIDR string
	VMIDStart, VLANStart                                   int
	InsecureSkipTLSVerify                                  bool
	OperatorCIDR                                           string
	Build                                                  lc.BuildConfig
}
type SetupPlan struct {
	RolePrivileges       []string       `json:"rolePrivileges"`
	Owner                string         `json:"owner"`
	Environment          lc.Environment `json:"environment"`
	BaseVMID             int            `json:"baseVMID"`
	ImageName            string         `json:"imageName"`
	SeedName             string         `json:"seedName"`
	StorageContentBefore string         `json:"storageContentBefore"`
	StorageContentAfter  string         `json:"storageContentAfter"`
	Changes              []string       `json:"changes"`
}
type setupState struct {
	TokenDisplayed bool              `json:"tokenDisplayed,omitempty"`
	Plan           SetupPlan         `json:"plan"`
	Steps          map[string]string `json:"steps"`
	Complete       bool              `json:"complete"`
	Removed        bool              `json:"removed"`
}
type Setup struct {
	seed   func(context.Context, string, string, string) (string, error)
	Client *Client
	File   string
	state  setupState
}

func OpenSetup(file string, opts SetupOptions) (*Setup, error) {
	absolute, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	s := &Setup{File: absolute}
	b, err := os.ReadFile(s.File + ".setup.json")
	if err == nil {
		if err = json.Unmarshal(b, &s.state); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.state.Plan.Owner != "" {
		if s.state.Steps == nil {
			return nil, fmt.Errorf("invalid setup receipt")
		}
		p := s.state.Plan.Environment.Proxmox
		if opts.URL != "" && opts.URL != p.URL {
			return nil, fmt.Errorf("setup receipt belongs to another endpoint")
		}
		opts.URL, opts.Node, opts.CAFile, opts.InsecureSkipTLSVerify = p.URL, p.Node, p.CAFile, p.InsecureSkipTLSVerify
	} else if _, err := os.Stat(s.File); err == nil {
		return nil, fmt.Errorf("environment exists without its setup receipt; refusing to overwrite")
	}
	if opts.AdminTokenEnv == "" {
		opts.AdminTokenEnv = "LABCENTRAL_PROXMOX_ADMIN_TOKEN"
	}
	e := lc.Environment{Proxmox: lc.ProxmoxConfig{URL: opts.URL, TokenID: opts.AdminTokenID, TokenEnv: opts.AdminTokenEnv, Node: opts.Node, CAFile: opts.CAFile, InsecureSkipTLSVerify: opts.InsecureSkipTLSVerify}}
	u, err := url.Parse(opts.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || opts.AdminTokenID == "" {
		return nil, fmt.Errorf("setup requires an HTTPS --url and --admin-token-id")
	}
	s.Client, err = NewClient(e)
	return s, err
}
func (s *Setup) save() error {
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return privatefile.Write(s.File+".setup.json", b)
}
func (s *Setup) Plan(ctx context.Context, o SetupOptions) (SetupPlan, error) {
	if s.state.Removed {
		s.state = setupState{}
	}
	if s.state.Plan.Owner != "" {
		plan := s.state.Plan
		if s.state.Complete {
			plan.Changes = []string{"Verify the recorded pool, user, role, token, internal bridge and base ownership; reuse completed setup", "Ensure scoped mutation ACLs and a read-only VM inventory role; refresh toolchain pins and preserve runtime policy"}
		}
		return plan, nil
	}
	if s.seed == nil {
		if _, err := exec.LookPath("xorriso"); err != nil {
			return SetupPlan{}, fmt.Errorf("setup needs xorriso: install it on the operator machine before provisioning")
		}
	}
	c := s.Client
	var nodes []struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if err := c.Call(ctx, "GET", "/nodes", nil, &nodes); err != nil {
		return SetupPlan{}, fmt.Errorf("admin token cannot list nodes: %w", err)
	}
	online := []string{}
	for _, n := range nodes {
		if n.Status == "online" {
			online = append(online, n.Node)
		}
	}
	if c.Node == "" {
		if len(online) != 1 {
			return SetupPlan{}, fmt.Errorf("choose --node: found %d online nodes", len(online))
		}
		c.Node = online[0]
	}
	if !lc.Contains(online, c.Node) {
		return SetupPlan{}, fmt.Errorf("selected node is not online")
	}
	var bridges []bridge
	if err := c.Call(ctx, "GET", c.nodePath("/network"), nil, &bridges); err != nil {
		return SetupPlan{}, err
	}
	if err := s.noPendingNetwork(ctx); err != nil {
		return SetupPlan{}, err
	}
	usedBridge := map[string]bool{}
	candidates := []string{}
	for _, b := range bridges {
		usedBridge[b.Interface] = true
		if b.Type == "bridge" && (b.Gateway != "" || b.Method == "dhcp") {
			candidates = append(candidates, b.Interface)
		}
	}
	if o.ManagementBridge == "" {
		if len(candidates) != 1 {
			return SetupPlan{}, fmt.Errorf("choose --management-bridge: expected one bridge with a default route/DHCP, found %d", len(candidates))
		}
		o.ManagementBridge = candidates[0]
	}
	if o.ManagementBridge == "" || !usedBridge[o.ManagementBridge] {
		return SetupPlan{}, fmt.Errorf("cannot detect management bridge; choose --management-bridge with DHCP/Internet and operator reachability")
	}
	if o.LabBridge == "" {
		for i := 0; i < 100; i++ {
			name := fmt.Sprintf("vmbr%d", i)
			if !usedBridge[name] {
				o.LabBridge = name
				break
			}
		}
	}
	if usedBridge[o.LabBridge] {
		return SetupPlan{}, fmt.Errorf("lab bridge already exists without this setup's ownership receipt; choose a free --lab-bridge")
	}
	var storages []struct {
		Storage string `json:"storage"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if err := c.Call(ctx, "GET", "/storage", nil, &storages); err != nil {
		return SetupPlan{}, err
	}
	sort.Slice(storages, func(i, j int) bool { return storages[i].Storage < storages[j].Storage })
	before := ""
	if o.Storage == "" {
		for _, st := range storages {
			if st.Type == "dir" && lc.Contains(strings.Split(st.Content, ","), "images") {
				o.Storage = st.Storage
				break
			}
		}
		if o.Storage == "" {
			for _, st := range storages {
				if st.Type == "dir" {
					o.Storage = st.Storage
					break
				}
			}
		}
	}
	for _, st := range storages {
		if st.Storage == o.Storage {
			if st.Type != "dir" {
				return SetupPlan{}, fmt.Errorf("setup currently needs directory storage; choose --storage with a directory backend")
			}
			before = st.Content
		}
	}
	if before == "" {
		return SetupPlan{}, fmt.Errorf("choose --storage: setup needs directory storage")
	}
	content := strings.Split(before, ",")
	for _, needed := range []string{"images", "iso", "import"} {
		if !lc.Contains(content, needed) {
			content = append(content, needed)
		}
	}
	sort.Strings(content)
	after := strings.Join(content, ",")
	if sameContent(before, after) {
		after = before
	}
	var inventory []struct {
		ID   int    `json:"vmid"`
		Node string `json:"node"`
		Type string `json:"type"`
	}
	if err := c.Call(ctx, "GET", "/cluster/resources?type=vm", nil, &inventory); err != nil {
		return SetupPlan{}, err
	}
	ids := map[int]bool{}
	vlans := map[int]bool{}
	prefixes := []netip.Prefix{}
	for _, b := range bridges {
		for _, address := range []string{b.CIDR, b.Address} {
			if p, err := netip.ParsePrefix(address); err == nil {
				prefixes = append(prefixes, p)
			} else if a, err := netip.ParseAddr(address); err == nil {
				prefixes = append(prefixes, netip.PrefixFrom(a, 32))
			}
		}
	}
	for _, v := range inventory {
		ids[v.ID] = true
		var cfg map[string]any
		if v.Type != "qemu" && v.Type != "lxc" {
			continue
		}
		path := "/nodes/" + url.PathEscape(v.Node) + "/" + v.Type + "/" + strconv.Itoa(v.ID) + "/config"
		if err := c.Call(ctx, "GET", path, nil, &cfg); err != nil {
			return SetupPlan{}, fmt.Errorf("admin token must inspect existing network allocations: %w", err)
		}
		for key, value := range cfg {
			str, _ := value.(string)
			if strings.HasPrefix(key, "net") {
				for _, part := range strings.Split(str, ",") {
					if tag, ok := strings.CutPrefix(part, "tag="); ok {
						n, _ := strconv.Atoi(tag)
						vlans[n] = true
					}
					if trunk, ok := strings.CutPrefix(part, "trunks="); ok {
						for _, tag := range strings.Split(trunk, ";") {
							loText, hiText, ranged := strings.Cut(tag, "-")
							if !ranged {
								hiText = loText
							}
							lo, le := strconv.Atoi(loText)
							hi, he := strconv.Atoi(hiText)
							if le != nil || he != nil || lo < 1 || hi < lo || hi > 4094 {
								return SetupPlan{}, fmt.Errorf("cannot inspect an existing VLAN trunk range")
							}
							for vlan := lo; vlan <= hi; vlan++ {
								vlans[vlan] = true
							}
						}
					}
				}
			}
			if strings.HasPrefix(key, "ipconfig") || v.Type == "lxc" && strings.HasPrefix(key, "net") {
				for _, part := range strings.Split(str, ",") {
					if ip, ok := strings.CutPrefix(part, "ip="); ok {
						if p, err := netip.ParsePrefix(ip); err == nil {
							prefixes = append(prefixes, p)
						}
					}
				}
			}
		}
	}
	free := func(start, count int, used map[int]bool) bool {
		for j := start; j < start+count; j++ {
			if used[j] {
				return false
			}
		}
		return true
	}
	if o.VMIDStart == 0 {
		for start := 2000; start < 999000; start += 100 {
			if free(start, 101, ids) {
				o.VMIDStart = start
				break
			}
		}
	}
	if o.VMIDStart < 100 || o.VMIDStart > 999999899 || !free(o.VMIDStart, 101, ids) {
		return SetupPlan{}, fmt.Errorf("VMID range overlaps existing guests; choose --vmid-start (101 free IDs)")
	}
	if o.VLANStart == 0 {
		for start := 100; start <= 3995; start += 100 {
			if free(start, 64, vlans) {
				o.VLANStart = start
				break
			}
		}
	}
	if o.VLANStart < 2 || o.VLANStart+63 > 4094 || !free(o.VLANStart, 64, vlans) {
		return SetupPlan{}, fmt.Errorf("VLAN range overlaps existing NICs or is outside 2–4094; choose --vlan-start (64 free tags; VLAN 1 is reserved for the native bridge)")
	}
	overlaps := func(p netip.Prefix) bool {
		for _, q := range prefixes {
			if p.Overlaps(q) {
				return true
			}
		}
		return false
	}
	// Include operator routes and addresses when selecting a disjoint private pool.
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			if p, err := netip.ParsePrefix(address.String()); err == nil && p.Addr().Is4() {
				prefixes = append(prefixes, p)
			}
		}
	}
	if o.CIDR == "" {
		for second := 80; second < 240; second++ {
			p := netip.MustParsePrefix(fmt.Sprintf("10.%d.0.0/16", second))
			if !overlaps(p) {
				o.CIDR = p.String()
				break
			}
		}
	}
	pool, err := netip.ParsePrefix(o.CIDR)
	if err != nil || !pool.Addr().Is4() || !pool.Addr().IsPrivate() || pool.Bits() > 18 || pool.Bits() < 16 || pool != pool.Masked() || overlaps(pool) {
		return SetupPlan{}, fmt.Errorf("CIDR pool must be canonical private /16–/18, disjoint from visible host/guest addresses")
	}
	if o.Pool == "" {
		o.Pool = "labcentral-v2"
	}
	if o.OperatorCIDR == "" {
		u, _ := url.Parse(o.URL)
		conn, err := net.Dial("udp4", net.JoinHostPort(u.Hostname(), "8006"))
		if err != nil {
			return SetupPlan{}, fmt.Errorf("set --operator-cidr to the operator's IPv4 /32")
		}
		o.OperatorCIDR = conn.LocalAddr().(*net.UDPAddr).IP.String() + "/32"
		conn.Close()
	}
	operator, err := netip.ParsePrefix(o.OperatorCIDR)
	if err != nil || !operator.Addr().Is4() || operator.Bits() < 24 {
		return SetupPlan{}, fmt.Errorf("operator CIDR must be IPv4 /24 or narrower")
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return SetupPlan{}, err
	}
	owner := hex.EncodeToString(random)
	e := lc.Environment{Version: 2, Name: "proxmox", Platform: "proxmox", OperatorCIDRs: []string{o.OperatorCIDR}, Bases: map[string]lc.Base{}, Build: o.Build, Proxmox: lc.ProxmoxConfig{URL: o.URL, Node: c.Node, Storage: o.Storage, Pool: o.Pool, LabBridge: o.LabBridge, ManagementBridge: o.ManagementBridge, VMIDStart: o.VMIDStart, VMIDEnd: o.VMIDStart + 99, VLANStart: o.VLANStart, VLANEnd: o.VLANStart + 63, CIDR: o.CIDR, TokenID: "labcentral@pve!labs", TokenEnv: "LABCENTRAL_PROXMOX_TOKEN", CAFile: o.CAFile, InsecureSkipTLSVerify: o.InsecureSkipTLSVerify}}
	e.Build.Bridge = o.ManagementBridge
	p := SetupPlan{RolePrivileges: setupPrivileges(), Owner: owner, Environment: e, BaseVMID: o.VMIDStart + 100, ImageName: "labcentral-" + owner + ".qcow2", SeedName: "labcentral-" + owner + ".iso", StorageContentBefore: before, StorageContentAfter: after}
	p.Changes = []string{fmt.Sprintf("Create owned pool %s, user labcentral@pve, role %s plus a read-only VM.Audit role, privilege-separated token labs and scoped ACLs", o.Pool, "LabCentralV2"+owner[:12]), fmt.Sprintf("Create %s: type bridge, autostart 1, bridge_vlan_aware 1, bridge_ports none, no address; apply only with no unrelated pending changes", o.LabBridge), fmt.Sprintf("Reserve VMIDs %d–%d, base %d; VLANs %d–%d; CIDR %s", e.Proxmox.VMIDStart, e.Proxmox.VMIDEnd, p.BaseVMID, e.Proxmox.VLANStart, e.Proxmox.VLANEnd, o.CIDR), "Download pinned Ubuntu image with SHA-256; boot owned seed ISO to install guest agent; sanitize and convert to base template", "Write environment, private receipt and token file; admin token is never stored"}
	if before != after {
		p.Changes = append([]string{"Storage content: " + before + " -> " + after}, p.Changes...)
	}
	return p, nil
}
func (s *Setup) noPendingNetwork(ctx context.Context) error {
	var changes string
	if err := s.Client.call(ctx, "GET", s.Client.nodePath("/network"), nil, nil, &changes); err != nil {
		return err
	}
	if strings.TrimSpace(changes) != "" {
		return fmt.Errorf("unrelated pending network changes exist; apply or revert them in Proxmox before setup/teardown")
	}
	return nil
}
func (s *Setup) Apply(ctx context.Context, plan SetupPlan) error {
	// Shared setup and teardown lock; a crashed process releases the OS lock.
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
		s.state = setupState{}
	}
	if s.state.Plan.Owner == "" {
		s.state = setupState{Plan: plan, Steps: map[string]string{}}
		if err = s.save(); err != nil {
			return err
		}
	}
	if s.state.Plan.Owner != plan.Owner {
		return fmt.Errorf("setup plan identity changed")
	}
	if s.state.Complete {
		if err = s.noPendingNetwork(ctx); err != nil {
			return err
		}
		for _, verify := range []func(context.Context) error{s.verifyPool, s.verifyUser, s.verifyRole, s.verifyToken, s.verifyBridge} {
			if err = verify(ctx); err != nil {
				return err
			}
		}
		if err = s.ensureACLs(ctx); err != nil {
			return err
		}
		v, err := s.Client.Config(ctx, s.state.Plan.BaseVMID)
		if err != nil {
			return err
		}
		if err = checkOwner(v, s.state.Plan.Owner, "base"); err != nil {
			return err
		}
		if v.Digest != s.state.Plan.Environment.Bases["ubuntu-24.04"].Identity {
			return fmt.Errorf("base identity changed; refusing setup rerun")
		}
		current, loadErr := lc.LoadEnvironment(s.File)
		if loadErr == nil {
			have, want := current.Proxmox, s.state.Plan.Environment.Proxmox
			have.AllowEgress = want.AllowEgress
			have.CPUTypes = want.CPUTypes
			have.ManagementAddresses = want.ManagementAddresses
			have.GatewayBase = want.GatewayBase
			base := current.Bases["ubuntu-24.04"]
			original := s.state.Plan.Environment.Bases["ubuntu-24.04"]
			if current.Platform != "proxmox" || !reflect.DeepEqual(have, want) || base.ID != original.ID || base.Identity != original.Identity {
				return fmt.Errorf("environment setup bindings changed; refusing to overwrite operator configuration")
			}
			s.state.Plan.Environment = current
		} else if !os.IsNotExist(loadErr) {
			return loadErr
		}
		s.state.Plan.Environment.Build = plan.Environment.Build
		if err = s.save(); err != nil {
			return err
		}
		return s.writeEnvironment()
	}
	if err = s.configureResources(ctx); err != nil {
		return err
	}
	if err = s.base(ctx); err != nil {
		return err
	}
	s.state.Complete = true
	if err = s.save(); err != nil {
		return err
	}
	return s.writeEnvironment()
}
func (s *Setup) writeEnvironment() error {
	b, err := yaml.Marshal(s.state.Plan.Environment)
	if err != nil {
		return err
	}
	return privatefile.Write(s.File, b)
}
func (s *Setup) step(ctx context.Context, key string, create func() error, verify func() error) error {
	status := s.state.Steps[key]
	if status != "" {
		if err := verify(); err != nil {
			return fmt.Errorf("setup %s %s; recover ownership before retry: %w", key, status, err)
		}
		if status == "done" {
			return nil
		}
	} else {
		s.state.Steps[key] = "intent"
		if err := s.save(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			delete(s.state.Steps, key)
			if saveErr := s.save(); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			return err
		}
		if err := create(); err != nil {
			if isHTTP(err, 400) || isHTTP(err, 401) || isHTTP(err, 403) {
				delete(s.state.Steps, key)
				if saveErr := s.save(); saveErr != nil {
					return errors.Join(err, saveErr)
				}
			}
			return err
		}
		if err := verify(); err != nil {
			return err
		}
	}
	s.state.Steps[key] = "done"
	return s.save()
}
func (s *Setup) ownedComment() string { return marker(s.state.Plan.Owner, "environment") }
func isHTTP(err error, status int) bool {
	var e *HTTPError
	return errors.As(err, &e) && e.Status == status
}
func (s *Setup) journal() journal.Journal { return journal.Journal{Root: s.File + ".setup-journal"} }

// TokenSecretOnce is used only for an interactive terminal, never progress logs.
func (s *Setup) TokenSecretOnce() (string, error) {
	st, err := store.Open(s.File + ".setup-state")
	if err != nil {
		return "", err
	}
	defer st.Close()
	unlock, err := st.LockKey("setup")
	if err != nil {
		return "", err
	}
	defer unlock()
	b, err := os.ReadFile(s.File + ".setup.json")
	if err != nil {
		return "", err
	}
	var current setupState
	if err = json.Unmarshal(b, &current); err != nil {
		return "", err
	}
	if current.Plan.Owner != s.state.Plan.Owner || current.Removed {
		return "", fmt.Errorf("setup identity changed")
	}
	s.state = current
	if s.state.TokenDisplayed {
		return "", nil
	}
	secret, err := os.ReadFile(s.File + ".token")
	if err != nil {
		return "", err
	}
	s.state.TokenDisplayed = true
	if err = s.save(); err != nil {
		return "", err
	}
	return string(secret), nil
}

func (s *Setup) reload() error {
	b, err := os.ReadFile(s.File + ".setup.json")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var current setupState
	if err = json.Unmarshal(b, &current); err != nil {
		return err
	}
	if current.Plan.Owner == "" || current.Steps == nil {
		return fmt.Errorf("invalid setup receipt")
	}
	s.state = current
	return nil
}
