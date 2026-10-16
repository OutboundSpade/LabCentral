package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"go.yaml.in/yaml/v3"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type setupAPI struct {
	acls             map[string]map[string]any
	mu               sync.Mutex
	bridges          map[string]bridge
	pools, users     map[string]string
	roles            map[string]map[string]int
	volumes          map[string]volume
	vms              map[int]VMConfig
	running          map[int]bool
	token            map[string]any
	pending, content string
	mutations        []string
}

func newSetupAPI() *setupAPI {
	return &setupAPI{acls: map[string]map[string]any{}, bridges: map[string]bridge{"vmbr0": {Interface: "vmbr0", Type: "bridge", Method: "dhcp", Ports: "eno1"}}, pools: map[string]string{}, users: map[string]string{}, roles: map[string]map[string]int{}, volumes: map[string]volume{}, vms: map[int]VMConfig{}, running: map[int]bool{}, content: "images,iso,import"}
}
func (f *setupAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api2/json")
	var in map[string]any
	if r.Method != "GET" && !strings.Contains(path, "/upload") {
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&in)
		}
		if in == nil {
			in = map[string]any{}
		}
	}
	if r.Method != "GET" {
		f.mutations = append(f.mutations, r.Method+" "+path)
	}
	emit := func(v any) { response(w, v) }
	integer := func(key string) int { n, _ := in[key].(float64); return int(n) }
	text := func(key string) string { s, _ := in[key].(string); return s }
	switch {
	case path == "/nodes":
		emit([]map[string]string{{"node": "node", "status": "online"}})
	case path == "/pools" && r.Method == "GET":
		items := []map[string]string{}
		for id := range f.pools {
			items = append(items, map[string]string{"poolid": id})
		}
		emit(items)
	case path == "/pools" && r.Method == "POST":
		f.pools[text("poolid")] = text("comment")
		emit(nil)
	case strings.HasPrefix(path, "/pools/"):
		id := strings.TrimPrefix(path, "/pools/")
		comment, ok := f.pools[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if r.Method == "DELETE" {
			delete(f.pools, id)
			emit(nil)
		} else {
			members := []map[string]any{}
			for id := range f.vms {
				members = append(members, map[string]any{"type": "qemu", "vmid": id})
			}
			emit(map[string]any{"comment": comment, "members": members})
		}
	case path == "/access/users" && r.Method == "GET":
		items := []map[string]string{}
		for id := range f.users {
			items = append(items, map[string]string{"userid": id})
		}
		emit(items)
	case path == "/access/users" && r.Method == "POST":
		f.users[text("userid")] = text("comment")
		emit(nil)
	case path == "/access/users/labcentral@pve":
		if r.Method == "DELETE" {
			delete(f.users, "labcentral@pve")
			f.token = nil
			emit(nil)
		} else {
			comment, ok := f.users["labcentral@pve"]
			if !ok {
				w.WriteHeader(404)
				return
			}
			emit(map[string]string{"comment": comment})
		}
	case path == "/access/roles" && r.Method == "GET":
		items := []map[string]string{}
		for id := range f.roles {
			items = append(items, map[string]string{"roleid": id})
		}
		emit(items)
	case path == "/access/roles" && r.Method == "POST":
		role := map[string]int{}
		for _, priv := range strings.Fields(text("privs")) {
			role[priv] = 1
		}
		f.roles[text("roleid")] = role
		emit(nil)
	case strings.HasPrefix(path, "/access/roles/"):
		id := strings.TrimPrefix(path, "/access/roles/")
		if r.Method == "DELETE" {
			delete(f.roles, id)
			emit(nil)
		} else {
			role, ok := f.roles[id]
			if !ok {
				w.WriteHeader(404)
				return
			}
			emit(role)
		}
	case path == "/access/users/labcentral@pve/token/labs":
		if r.Method == "POST" {
			f.token = map[string]any{"comment": text("comment"), "privsep": integer("privsep")}
			emit(map[string]string{"value": "runtime-secret-never-in-receipt"})
		} else {
			emit(f.token)
		}
	case path == "/access/users/labcentral@pve/token":
		items := []map[string]any{}
		if f.token != nil {
			items = append(items, map[string]any{"tokenid": "labs", "comment": f.token["comment"], "privsep": f.token["privsep"]})
		}
		emit(items)
	case path == "/access/acl":
		if r.Method == "PUT" {
			id := text("users")
			kind := "user"
			if id == "" {
				id = text("tokens")
				kind = "token"
			}
			key := text("path") + "|" + id + "|" + text("roles")
			if integer("delete") == 1 {
				delete(f.acls, key)
			} else {
				f.acls[key] = map[string]any{"path": text("path"), "ugid": id, "roleid": text("roles"), "type": kind}
			}
			emit(nil)
		} else {
			items := []map[string]any{}
			for _, a := range f.acls {
				items = append(items, a)
			}
			emit(items)
		}
	case path == "/access/permissions":
		emit(map[string]any{"/": map[string]int{"VM.Audit": 1, "Datastore.Audit": 1}})
	case path == "/storage":
		emit([]map[string]string{{"storage": "local", "content": f.content, "type": "dir"}})
	case path == "/storage/local":
		if r.Method == "PUT" {
			f.content = text("content")
		}
		emit(map[string]string{"content": f.content})
	case path == "/cluster/resources":
		items := []map[string]any{}
		for id := range f.vms {
			items = append(items, map[string]any{"vmid": id, "node": "node", "type": "qemu"})
		}
		emit(items)
	case path == "/nodes/node/network":
		if r.Method == "GET" {
			items := []bridge{}
			for _, v := range f.bridges {
				items = append(items, v)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": items, "changes": f.pending})
			return
		}
		if r.Method == "PUT" {
			f.pending = ""
			emit("UPID:node:test")
			return
		}
		name := text("iface")
		f.bridges[name] = bridge{Interface: name, Type: "bridge", VLANAware: 1, Comments: text("comments")}
		f.pending = fmt.Sprintf("--- old\n+++ new\n+auto %s\n+iface %s inet manual\n+ bridge-ports none\n+ bridge-vlan-aware yes\n+#%s\n", name, name, text("comments"))
		emit(nil)
	case strings.HasPrefix(path, "/nodes/node/network/"):
		name := strings.TrimPrefix(path, "/nodes/node/network/")
		v, ok := f.bridges[name]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if r.Method == "DELETE" {
			delete(f.bridges, name)
			f.pending = fmt.Sprintf("--- old\n+++ new\n-auto %s\n-iface %s inet manual\n- bridge-ports none\n- bridge-vlan-aware yes\n-#%s\n", name, name, v.Comments)
			emit(nil)
		} else {
			v.Interface = "" // The real single-interface endpoint omits iface.
			emit(v)
		}
	case strings.Contains(path, "/tasks/"):
		emit(map[string]string{"status": "stopped", "exitstatus": "OK"})
	case path == "/nodes/node/storage/local/download-url":
		id := "local:import/" + text("filename")
		f.volumes[id] = volume{ID: id, Size: 100, Format: "qcow2", Content: "import", CTime: 1}
		emit("UPID:node:test")
	case path == "/nodes/node/storage/local/upload":
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			w.WriteHeader(400)
			return
		}
		file, header, err := r.FormFile("filename")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		file.Close()
		id := "local:iso/" + header.Filename
		f.volumes[id] = volume{ID: id, Size: 100, Format: "iso", Content: "iso", CTime: 1}
		emit("UPID:node:test")
	case path == "/nodes/node/storage/local/content":
		items := []volume{}
		for _, v := range f.volumes {
			items = append(items, v)
		}
		emit(items)
	case strings.HasPrefix(path, "/nodes/node/storage/local/content/"):
		id := strings.TrimPrefix(path, "/nodes/node/storage/local/content/")
		delete(f.volumes, id)
		emit("UPID:node:test")
	case path == "/nodes/node/qemu" && r.Method == "GET":
		items := []map[string]int{}
		for id := range f.vms {
			items = append(items, map[string]int{"vmid": id})
		}
		emit(items)
	case path == "/nodes/node/qemu" && r.Method == "POST":
		id := integer("vmid")
		f.vms[id] = VMConfig{Description: text("description"), Digest: "identity", Agent: "enabled=1", Scsi0: "local:disk", Ide2: text("ide2"), Net0: "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0"}
		emit("UPID:node:test")
	case strings.HasPrefix(path, "/nodes/node/qemu/"):
		tail := strings.TrimPrefix(path, "/nodes/node/qemu/")
		parts := strings.SplitN(tail, "/", 2)
		id, _ := strconv.Atoi(parts[0])
		v, ok := f.vms[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		suffix := ""
		if len(parts) > 1 {
			suffix = "/" + parts[1]
		}
		switch suffix {
		case "":
			if r.Method == "DELETE" {
				delete(f.vms, id)
				emit("UPID:node:test")
			} else {
				w.WriteHeader(400)
			}
		case "/config":
			if r.Method == "PUT" {
				if text("ide2") != "" {
					v.Ide2 = text("ide2")
				}
				f.vms[id] = v
				emit(nil)
			} else {
				emit(v)
			}
		case "/status/current":
			status := "stopped"
			if f.running[id] {
				status = "running"
			}
			emit(map[string]string{"status": status})
		case "/status/start":
			f.running[id] = true
			emit("UPID:node:test")
		case "/status/stop", "/status/shutdown":
			f.running[id] = false
			emit("UPID:node:test")
		case "/agent/exec":
			emit(map[string]int{"pid": 1})
		case "/agent/exec-status":
			emit(map[string]int{"exited": 1, "exitcode": 0})
		case "/agent/ping", "/resize":
			emit(nil)
		case "/template":
			v.Template = 1
			f.vms[id] = v
			emit("UPID:node:test")
		default:
			w.WriteHeader(404)
			t := suffix
			_ = t
		}
	default:
		w.WriteHeader(404)
	}
}
func setupFixture(t *testing.T) (*Setup, SetupOptions, *setupAPI) {
	t.Helper()
	f := newSetupAPI()
	c, e := testClient(t, f.serve)
	file := filepath.Join(t.TempDir(), "environment.yaml")
	opts := SetupOptions{URL: e.Proxmox.URL, Node: "node", AdminTokenID: e.Proxmox.TokenID, AdminTokenEnv: e.Proxmox.TokenEnv, InsecureSkipTLSVerify: true, OperatorCIDR: "192.0.2.10/32"}
	s, err := OpenSetup(file, opts)
	if err != nil {
		t.Fatal(err)
	}
	s.Client = c
	s.seed = func(ctx context.Context, dir, owner, data string) (string, error) {
		if strings.Contains(data, "runtime-secret") || !strings.Contains(data, "qemu-guest-agent") {
			t.Error("wrong bootstrap data")
		}
		file := filepath.Join(dir, "seed.iso")
		return file, os.WriteFile(file, []byte("fixture ISO"), 0600)
	}
	return s, opts, f
}
func TestSetupTeardownIdempotenceAndPrivateReceipts(t *testing.T) {
	ctx := context.Background()
	s, o, f := setupFixture(t)
	plan, err := s.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.mutations) != 0 {
		t.Fatal("planning mutated host")
	}
	if err = s.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if role := f.roles[s.readRole()]; len(role) != 1 || role["VM.Audit"] != 1 {
		t.Fatal("global inventory role must be read-only", role)
	}
	for _, acl := range f.acls {
		if acl["path"] == "/vms" && acl["roleid"] != s.readRole() {
			t.Fatal("global inventory grants mutation rights", acl)
		}
	}
	if plan.BaseVMID <= plan.Environment.Proxmox.VMIDEnd {
		t.Fatal("base overlaps runtime allocation")
	}
	for _, path := range []string{s.File, s.File + ".token", s.File + ".setup.json"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal(path, info, err)
		}
	}
	data, _ := os.ReadFile(s.File + ".setup.json")
	if strings.Contains(string(data), "runtime-secret") {
		t.Fatal("secret leaked into receipt")
	}
	before := len(f.mutations)
	again, err := OpenSetup(s.File, o)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = again.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = again.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if len(f.mutations) != before {
		t.Fatal("completed setup repeated native effects")
	}
	if err = again.Teardown(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.vms) != 0 || len(f.volumes) != 0 || len(f.pools) != 0 || len(f.users) != 0 || len(f.roles) != 0 || len(f.bridges) != 1 || f.bridges["vmbr0"].Ports != "eno1" {
		t.Fatal("teardown leaked or touched an existing bridge", f)
	}
	reopened, err := OpenSetup(s.File, o)
	if err != nil {
		t.Fatal(err)
	}
	reopened.seed = s.seed
	if err = reopened.Teardown(ctx); err != nil {
		t.Fatal("teardown rerun", err)
	}
	plan, err = reopened.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Owner == s.state.Plan.Owner {
		t.Fatal("clean setup reused old ownership")
	}
}
func TestSetupRefusesForeignAndUnrelatedPendingChanges(t *testing.T) {
	for _, scenario := range []string{"pending", "pool", "user", "vlan", "bridge"} {
		t.Run(scenario, func(t *testing.T) {
			s, o, f := setupFixture(t)
			switch scenario {
			case "pending":
				f.pending = "+iface unrelated inet static"
			case "pool":
				f.pools["labcentral-v2"] = "somebody else"
			case "user":
				f.users["labcentral@pve"] = "somebody else"
			case "vlan":
				o.VLANStart = 100
				f.vms[300] = VMConfig{Net0: "virtio=AA:BB,bridge=vmbr0,tag=100"}
			case "bridge":
				o.LabBridge = "vmbr0"
			}
			plan, err := s.Plan(context.Background(), o)
			if err == nil && (scenario == "pool" || scenario == "user") {
				err = s.Apply(context.Background(), plan)
			}
			if err == nil {
				t.Fatal("unsafe setup accepted", scenario)
			}
			if f.bridges["vmbr0"].Ports != "eno1" {
				t.Fatal("existing bridge changed")
			}
		})
	}
}
func TestNetworkDiffCannotApplyUnrelatedChange(t *testing.T) {
	s, _, _ := setupFixture(t)
	s.state.Plan.Owner = strings.Repeat("a", 32)
	s.state.Plan.Environment.Proxmox.LabBridge = "vmbr3"
	good := "--- old\n+++ new\n+auto vmbr3\n+iface vmbr3 inet manual\n+ bridge-ports none\n+ bridge-vlan-aware yes\n+#" + s.ownedComment() + "\n"
	if err := s.checkNetworkDiff(good, false); err != nil {
		t.Fatal(err)
	}
	for _, diff := range []string{
		good + "+address 192.0.2.10\n", good + "-iface vmbr0 inet dhcp\n", good + "+bridge-ports eno1\n",
		good + " iface vmbr0 inet manual\n+ bridge-vlan-aware yes\n",
		" iface vmbr0 inet manual\n+ bridge-ports none\n" + good,
		good + "@@ -40,2 +40,3 @@\n bridge-fd 0\n+ bridge-vlan-aware yes\n",
	} {
		if err := s.checkNetworkDiff(diff, false); err == nil {
			t.Fatal("unrelated network delta accepted")
		}
	}
}
func TestTeardownRefusesOwnershipChange(t *testing.T) {
	ctx := context.Background()
	s, o, f := setupFixture(t)
	plan, err := s.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	f.pools[plan.Environment.Proxmox.Pool] = "new owner"
	before := len(f.mutations)
	if err = s.Teardown(ctx); err == nil || !strings.Contains(err.Error(), "ownership_mismatch") {
		t.Fatal(err)
	}
	if len(f.mutations) != before {
		t.Fatal("teardown mutated after ownership change")
	}
}

func TestSetupUnknownCreateRequiresDiscoveryAndDeniedCreateCanRetry(t *testing.T) {
	s, _, _ := setupFixture(t)
	s.state.Plan.Owner = strings.Repeat("a", 32)
	s.state.Steps = map[string]string{}
	attempts := 0
	found := false
	create := func() error { attempts++; return fmt.Errorf("transport response lost") }
	verify := func() error {
		if !found {
			return fmt.Errorf("not found yet")
		}
		return nil
	}
	if err := s.step(context.Background(), "uncertain", create, verify); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	if err := s.step(context.Background(), "uncertain", create, verify); err == nil || attempts != 1 {
		t.Fatal("unknown create replayed", attempts, err)
	}
	found = true
	if err := s.step(context.Background(), "uncertain", create, verify); err != nil || attempts != 1 {
		t.Fatal("discovery failed", attempts, err)
	}
	denied := func() error { attempts++; return &HTTPError{400, "POST", "roles"} }
	if err := s.step(context.Background(), "denied", denied, verify); err == nil {
		t.Fatal("denial accepted")
	}
	if s.state.Steps["denied"] != "" {
		t.Fatal("authoritative denial became uncertain")
	}
}

func TestCompetingSetupCannotReplaceOwnershipReceipt(t *testing.T) {
	ctx := context.Background()
	first, o, _ := setupFixture(t)
	second, err := OpenSetup(first.File, o)
	if err != nil {
		t.Fatal(err)
	}
	second.seed = first.seed
	p1, err := first.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := second.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Apply(ctx, p1); err != nil {
		t.Fatal(err)
	}
	if err = second.Apply(ctx, p2); err == nil || !strings.Contains(err.Error(), "plan identity changed") {
		t.Fatal("competing plan replaced receipt", err)
	}
	data, _ := os.ReadFile(first.File + ".setup.json")
	if !strings.Contains(string(data), p1.Owner) || strings.Contains(string(data), p2.Owner) {
		t.Fatal("ownership receipt lost")
	}
}

func TestSetupRestoresOnlyItsStorageContentChange(t *testing.T) {
	ctx := context.Background()
	s, o, f := setupFixture(t)
	f.content = "images,iso"
	plan, err := s.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if sameContent(plan.StorageContentBefore, plan.StorageContentAfter) {
		t.Fatal("missing import content change")
	}
	if err = s.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.content, "import") {
		t.Fatal("import was not enabled")
	}
	if err = s.Teardown(ctx); err != nil {
		t.Fatal(err)
	}
	if !sameContent(f.content, "images,iso") {
		t.Fatal("storage content not restored", f.content)
	}
}
func TestBootstrapImagePinsMatchToolchain(t *testing.T) {
	data, err := os.ReadFile("../../tools/toolchain.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins struct {
		Base struct {
			URL string `json:"url"`
			SHA string `json:"sha256"`
		} `json:"proxmoxBase"`
	}
	if err = json.Unmarshal(data, &pins); err != nil {
		t.Fatal(err)
	}
	if pins.Base.URL != UbuntuCloudURL || pins.Base.SHA != UbuntuCloudSHA256 {
		t.Fatal("bootstrap pins drifted from toolchain")
	}
}

func TestSetupRerunPreservesRuntimePolicy(t *testing.T) {
	ctx := context.Background()
	s, o, _ := setupFixture(t)
	plan, err := s.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	e := s.state.Plan.Environment
	e.Proxmox.AllowEgress = true
	e.Proxmox.CPUTypes = []string{"host"}
	e.OperatorCIDRs = []string{"198.51.100.10/32"}
	data, err := yaml.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.File, data, 0600); err != nil {
		t.Fatal(err)
	}
	again, err := OpenSetup(s.File, o)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = again.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = again.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	e, err = lc.LoadEnvironment(s.File)
	if err != nil || !e.Proxmox.AllowEgress || len(e.Proxmox.CPUTypes) != 1 || e.OperatorCIDRs[0] != "198.51.100.10/32" {
		t.Fatal("setup reset operator runtime policy", e, err)
	}
}

func TestSetupTokenIsDisplayedOnlyOnce(t *testing.T) {
	ctx := context.Background()
	s, o, _ := setupFixture(t)
	plan, err := s.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	secret, err := s.TokenSecretOnce()
	if err != nil || !strings.Contains(secret, "runtime-secret") {
		t.Fatal("first display failed", err)
	}
	again, err := OpenSetup(s.File, o)
	if err != nil {
		t.Fatal(err)
	}
	secret, err = again.TokenSecretOnce()
	if err != nil || secret != "" {
		t.Fatal("secret was displayed again", err)
	}
	receipt, _ := os.ReadFile(s.File + ".setup.json")
	if strings.Contains(string(receipt), "runtime-secret") {
		t.Fatal("secret persisted in receipt")
	}
}
func TestTeardownRefusesModifiedTokenBeforeAnyMutation(t *testing.T) {
	ctx := context.Background()
	s, o, f := setupFixture(t)
	plan, err := s.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	f.token["comment"] = "somebody else"
	before := len(f.mutations)
	if err = s.Teardown(ctx); err == nil || !strings.Contains(err.Error(), "ownership_mismatch") {
		t.Fatal(err)
	}
	if len(f.mutations) != before {
		t.Fatal("modified token was removed")
	}
}
func TestSetupCanEnableImagesOnFreshDirectoryStorage(t *testing.T) {
	ctx := context.Background()
	s, o, f := setupFixture(t)
	f.content = "iso,backup"
	plan, err := s.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.StorageContentAfter, "images") || !strings.Contains(plan.StorageContentAfter, "import") {
		t.Fatal("missing reviewed content enablement", plan)
	}
	if err = s.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err = s.Teardown(ctx); err != nil {
		t.Fatal(err)
	}
	if !sameContent(f.content, "iso,backup") {
		t.Fatal("storage settings were not restored", f.content)
	}
}

func TestSetupRefusesAmbiguousDefaultBridgeAndTrunkOverlap(t *testing.T) {
	s, o, f := setupFixture(t)
	f.bridges["vmbr5"] = bridge{Interface: "vmbr5", Type: "bridge", Method: "dhcp"}
	if _, err := s.Plan(context.Background(), o); err == nil || !strings.Contains(err.Error(), "--management-bridge") {
		t.Fatal("ambiguous bridge accepted", err)
	}
	o.ManagementBridge = "vmbr0"
	o.VLANStart = 100
	f.vms[300] = VMConfig{Net0: "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0,trunks=100-200"}
	if _, err := s.Plan(context.Background(), o); err == nil || !strings.Contains(err.Error(), "VLAN range overlaps") {
		t.Fatal("trunk overlap accepted", err)
	}
	if len(f.mutations) != 0 {
		t.Fatal("planning mutated host")
	}
}

func TestTeardownRefusesModifiedInventoryRoleBeforeMutation(t *testing.T) {
	ctx := context.Background()
	s, o, f := setupFixture(t)
	plan, err := s.Plan(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	f.roles[s.readRole()]["VM.PowerMgmt"] = 1
	before := len(f.mutations)
	if err = s.Teardown(ctx); err == nil || !strings.Contains(err.Error(), "inventory role changed") {
		t.Fatalf("unexpected: %v", err)
	}
	if len(f.mutations) != before {
		t.Fatal("teardown mutated before identity ownership verification")
	}
}

func TestSetupRefusesMissingSeedToolBeforeProvisioning(t *testing.T) {
	s, o, f := setupFixture(t)
	s.seed = nil
	t.Setenv("PATH", t.TempDir())
	if _, err := s.Plan(context.Background(), o); err == nil || !strings.Contains(err.Error(), "install it on the operator") {
		t.Fatal(err)
	}
	if len(f.mutations) != 0 {
		t.Fatal("missing local tool left partial infrastructure")
	}
}
