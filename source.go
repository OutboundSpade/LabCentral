package labcentral

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
var basePattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)
var sizePattern = regexp.MustCompile(`^([1-9][0-9]*)(Mi|Gi)$`)

func SizeMiB(s string) (int, error) {
	m := sizePattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("expected a positive Mi or Gi size")
	}
	n, e := strconv.Atoi(m[1])
	if e != nil || n > 1048576 {
		return 0, fmt.Errorf("size exceeds supported bound")
	}
	if m[2] == "Gi" {
		n *= 1024
	}
	return n, nil
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func strictYAML(data []byte, out any) error {
	if len(data) > 1<<20 {
		return fmt.Errorf("YAML exceeds 1 MiB")
	}
	var root yaml.Node
	if e := yaml.Unmarshal(data, &root); e != nil {
		return e
	}
	var visit func(*yaml.Node, int) error
	visit = func(n *yaml.Node, d int) error {
		if d > 32 {
			return fmt.Errorf("YAML nesting exceeds 32")
		}
		if n.Kind == yaml.AliasNode || n.Anchor != "" {
			return fmt.Errorf("YAML aliases and anchors are unsupported")
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Tag != "!!str" || seen[k.Value] {
					return fmt.Errorf("line %d: duplicate or non-string key %q", k.Line, k.Value)
				}
				seen[k.Value] = true
			}
		}
		for _, c := range n.Content {
			if e := visit(c, d+1); e != nil {
				return e
			}
		}
		return nil
	}
	if e := visit(&root, 0); e != nil {
		return e
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if e := d.Decode(out); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return fmt.Errorf("expected exactly one YAML document")
	}
	return nil
}
func Load(dir string) (Source, error) {
	var s Source
	root, e := filepath.Abs(dir)
	if e != nil {
		return s, e
	}
	info, e := os.Lstat(root)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return s, problem(Invalid, dir, "expected a real directory")
	}
	total := 0
	e = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return fmt.Errorf("hidden file %s is not allowed in lab source", rel)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: symlinks are forbidden", rel)
		}
		i, err := d.Info()
		if err != nil {
			return err
		}
		if !i.Mode().IsRegular() {
			return fmt.Errorf("%s: expected a regular file", rel)
		}
		if i.Size() > 8<<20 || total+int(i.Size()) > 32<<20 || len(s.Files) >= 256 {
			return fmt.Errorf("source exceeds file/size limits")
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		total += len(b)
		s.Files = append(s.Files, File{filepath.ToSlash(rel), b})
		return nil
	})
	if e != nil {
		return s, e
	}
	for _, f := range s.Files {
		if f.Path == "lab.yaml" {
			if e := strictYAML(f.Content, &s.Lab); e != nil {
				return s, problem(Invalid, "lab.yaml", e.Error())
			}
		}
	}
	s.Digest = digest(s.Files)
	return s, Validate(s)
}
func Validate(s Source) error {
	l := s.Lab
	if l.Version != 2 {
		return problem(Invalid, "version", "expected 2")
	}
	if !namePattern.MatchString(l.Name) {
		return problem(Invalid, "name", "use a lowercase name of at most 48 characters")
	}
	if len(l.Machines) == 0 || len(l.Machines) > 32 {
		return problem(Invalid, "machines", "expected 1–32 machines")
	}
	if len(l.Images) == 0 || len(l.Images) > 32 {
		return problem(Invalid, "images", "expected 1–32 images")
	}
	if l.Network.Internet {
		return problem(Unsupported, "network.internet", "general runtime Internet egress is not implemented")
	}
	if digest(s.Files) != s.Digest {
		return problem(Invalid, "source", "source digest mismatch")
	}
	var declared Lab
	foundDeclaration := false
	for _, f := range s.Files {
		if f.Path == "lab.yaml" {
			if err := strictYAML(f.Content, &declared); err != nil {
				return problem(Invalid, "lab.yaml", err.Error())
			}
			foundDeclaration = true
		}
	}
	if !foundDeclaration || digest(declared) != digest(l) {
		return problem(Invalid, "source", "parsed declaration differs from source files")
	}
	paths := map[string]bool{}
	for _, f := range s.Files {
		if !fs.ValidPath(f.Path) || !regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`).MatchString(f.Path) || paths[f.Path] {
			return problem(Invalid, f.Path, "unsafe or duplicate source path")
		}
		paths[f.Path] = true
	}
	for n, i := range l.Images {
		if !namePattern.MatchString(n) || !basePattern.MatchString(i.Base) {
			return problem(Invalid, "images."+n, "invalid name or base")
		}
		if i.Playbook != "" && !paths[i.Playbook] {
			return problem(Invalid, "images."+n+".playbook", "file is not part of the source")
		}
	}
	for n, m := range l.Machines {
		f := "machines." + n
		if !namePattern.MatchString(n) {
			return problem(Invalid, f, "invalid name")
		}
		if _, ok := l.Images[m.Image]; !ok {
			return problem(Invalid, f+".image", "unknown image")
		}
		if m.CPU < 1 || m.CPU > 64 {
			return problem(Invalid, f+".cpu", "expected 1–64")
		}
		mem, e := SizeMiB(m.Memory)
		if e != nil || mem < 256 || mem > 262144 {
			return problem(Invalid, f+".memory", "expected 256Mi–256Gi")
		}
		disk, e := SizeMiB(m.Disk)
		if e != nil || disk%1024 != 0 || disk < 1024 || disk > 1048576 {
			return problem(Invalid, f+".disk", "expected 1Gi–1024Gi")
		}
		seen := map[string]bool{}
		for _, a := range m.Access {
			if (a != "ssh" && a != "desktop") || seen[a] {
				return problem(Invalid, f+".access", "use unique ssh or desktop entries")
			}
			seen[a] = true
		}
	}
	for n, i := range l.Inputs {
		if !namePattern.MatchString(n) || !Contains([]string{"string", "integer", "boolean"}, i.Type) {
			return problem(Invalid, "inputs."+n, "invalid name or type")
		}
		if i.Default != "" {
			if e := checkInput(i.Type, i.Default); e != nil {
				return problem(Invalid, "inputs."+n, e.Error())
			}
		}
	}
	return nil
}
func checkInput(t, v string) error {
	switch t {
	case "integer":
		_, e := strconv.ParseInt(v, 10, 64)
		return e
	case "boolean":
		if v != "true" && v != "false" {
			return fmt.Errorf("expected true or false")
		}
	case "string":
		if len(v) > 4096 {
			return fmt.Errorf("input exceeds 4096 bytes")
		}
	default:
		return fmt.Errorf("unknown input type")
	}
	return nil
}
func LoadEnvironment(path string) (Environment, error) {
	var e Environment
	b, err := os.ReadFile(path)
	if err != nil {
		return e, err
	}
	if err = strictYAML(b, &e); err != nil {
		return e, err
	}
	return e, ValidateEnvironment(e)
}
func ValidateEnvironment(e Environment) error {
	bad := func(f, m string) error { return problem(Invalid, "environment."+f, m) }
	if e.Version != 2 || !namePattern.MatchString(e.Name) {
		return bad("version/name", "expected version 2 and a lowercase name")
	}
	if e.Platform != "aws" && e.Platform != "proxmox" {
		return bad("platform", "expected aws or proxmox")
	}
	if len(e.OperatorCIDRs) == 0 {
		return bad("operatorCIDRs", "at least one explicit operator range is required")
	}
	for _, s := range e.OperatorCIDRs {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() || p.Bits() < 24 {
			return bad("operatorCIDRs", "use IPv4 ranges of /24 or narrower")
		}
	}
	for n, b := range e.Bases {
		if !basePattern.MatchString(n) || b.ID == "" || b.Identity == "" || !namePattern.MatchString(b.User) || b.DiskGiB < 1 {
			return bad("bases."+n, "id, immutable identity, user and diskGiB are required")
		}
	}
	if e.Platform == "aws" {
		a := e.AWS
		if !regexp.MustCompile(`^(us|eu|ap|ca|sa|me|af|il|mx)-[a-z]+-[0-9]+$`).MatchString(a.Region) || !strings.HasPrefix(a.Zone, a.Region) || !regexp.MustCompile(`^[0-9]{12}$`).MatchString(a.Account) || len(a.Types) == 0 {
			return bad("aws", "account, region, zone and instance type allowlist are required")
		}
		for _, t := range a.Types {
			if t.Name == "" || t.CPU < 1 || t.MemoryMiB < 256 {
				return bad("aws.types", "invalid instance type capacity")
			}
		}
	} else {
		p := e.Proxmox
		u, err := url.Parse(p.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return bad("proxmox.url", "expected HTTPS endpoint without credentials/query")
		}
		if p.Node == "" || p.Storage == "" || p.Pool == "" || p.Bridge == "" || p.TokenID == "" || p.TokenEnv == "" {
			return bad("proxmox", "node, storage, pool, bridge and token reference are required")
		}
		if p.VMIDStart < 100 || p.VMIDEnd < p.VMIDStart || p.VMIDEnd > 999999999 || p.VLANStart < 1 || p.VLANEnd < p.VLANStart || p.VLANEnd > 4094 {
			return bad("proxmox", "invalid VMID or VLAN range")
		}
		cidr, err := netip.ParsePrefix(p.CIDR)
		if err != nil || !cidr.Addr().Is4() || cidr.Bits() > 24 || cidr.Bits() < 16 {
			return bad("proxmox.cidr", "expected IPv4 /16–/24")
		}
		if _, _, err := ProxmoxSubnet(p, p.VLANEnd); err != nil {
			return bad("proxmox.cidr", err.Error())
		}
		if !p.VerifiedIsolation {
			return bad("proxmox.verifiedIsolation", "operator must verify VLAN and management isolation before enabling")
		}
	}
	return nil
}
func PlanLab(s Source, e Environment, values map[string]string) (Plan, error) {
	p := Plan{Version: 2, Source: s, Environment: e, EnvironmentDigest: digest(e)}
	if err := Validate(s); err != nil {
		return p, err
	}
	if err := ValidateEnvironment(e); err != nil {
		return p, err
	}
	for n := range values {
		if _, ok := s.Lab.Inputs[n]; !ok {
			return p, problem(Invalid, "inputs."+n, "undeclared input")
		}
	}
	for _, n := range sortedKeys(s.Lab.Inputs) {
		d := s.Lab.Inputs[n]
		v, ok := values[n]
		if !ok {
			v = d.Default
			if d.Required && v == "" {
				return p, problem(Invalid, "inputs."+n, "required")
			}
		}
		if err := checkInput(d.Type, v); err != nil {
			return p, problem(Invalid, "inputs."+n, err.Error())
		}
		p.Inputs = append(p.Inputs, InputValue{n, d.Type, v})
	}
	for _, n := range sortedKeys(s.Lab.Images) {
		i := s.Lab.Images[n]
		b, ok := e.Bases[i.Base]
		if !ok {
			return p, problem(Invalid, "images."+n+".base", "no environment binding")
		}
		im := PlannedImage{Name: n, Base: b, Playbook: i.Playbook, DiskGiB: b.DiskGiB}
		minDisk := 0
		for _, m := range s.Lab.Machines {
			if m.Image == n {
				im.Desktop = im.Desktop || Contains(m.Access, "desktop")
				disk, _ := SizeMiB(m.Disk)
				if minDisk == 0 || disk/1024 < minDisk {
					minDisk = disk / 1024
				}
			}
		}
		if im.Desktop && i.Playbook == "" {
			return p, problem(Unsupported, "images."+n+".playbook", "desktop requires an image build with the platform access components")
		}
		if minDisk > 0 && e.Platform == "aws" {
			im.DiskGiB = minDisk
		}
		if i.Playbook != "" {
			if e.Build.Packer == "" || len(e.Build.PackerSHA256) != 64 || e.Build.ToolchainID == "" || e.Build.AnsibleVersion == "" || e.Build.PluginDirectory == "" || e.Build.TimeoutSeconds < 60 {
				return p, problem(Invalid, "environment.build", "pinned Packer, plugin directory, toolchain ID, Ansible version and timeout are required")
			}
		}
		im.CacheKey = digest(struct {
			S string
			I PlannedImage
			E Environment
			V []InputValue
		}{s.Digest, im, e, p.Inputs})
		p.Images = append(p.Images, im)
	}
	for _, n := range sortedKeys(s.Lab.Machines) {
		m := s.Lab.Machines[n]
		mem, _ := SizeMiB(m.Memory)
		disk, _ := SizeMiB(m.Disk)
		b := e.Bases[s.Lab.Images[m.Image].Base]
		if disk/1024 < b.DiskGiB {
			return p, problem(Invalid, "machines."+n+".disk", "cannot shrink the base disk")
		}
		pm := PlannedMachine{Name: n, Spec: m, CPU: m.CPU, MemoryMiB: mem, DiskGiB: disk / 1024}
		if e.Platform == "aws" {
			types := append([]InstanceType(nil), e.AWS.Types...)
			sort.Slice(types, func(i, j int) bool {
				if types[i].CPU != types[j].CPU {
					return types[i].CPU < types[j].CPU
				}
				if types[i].MemoryMiB != types[j].MemoryMiB {
					return types[i].MemoryMiB < types[j].MemoryMiB
				}
				return types[i].Name < types[j].Name
			})
			for _, t := range types {
				if t.CPU >= m.CPU && t.MemoryMiB >= mem {
					pm.InstanceType = t.Name
					pm.CPU = t.CPU
					pm.MemoryMiB = t.MemoryMiB
					break
				}
			}
			if pm.InstanceType == "" {
				return p, problem(Unsupported, "machines."+n, "no allowed instance type satisfies requirements")
			}
			if Contains(m.Access, "desktop") && e.AWS.DesktopProfile == "" {
				return p, problem(Invalid, "environment.aws.desktopProfile", "DCV needs a license-only instance profile")
			}
		}
		p.Machines = append(p.Machines, pm)
	}
	if e.Platform == "proxmox" {
		builds := 0
		for _, im := range p.Images {
			if im.Playbook != "" {
				builds++
			}
		}
		required := len(p.Machines) + builds
		if builds+1 > required {
			required = builds + 1
		}
		if e.Proxmox.VMIDEnd-e.Proxmox.VMIDStart+1 < required {
			return p, problem(Unsupported, "environment.proxmox.vmidEnd", "VMID range is too small for images, validation and deployment")
		}
	}
	p.Notices = []string{"Instances and native images remain billable until removed; no background cleanup service is installed.", "Builds may access package repositories; runtime general Internet egress is denied."}
	if e.Platform == "aws" {
		p.Notices = append(p.Notices, "Public IPv4 is billed. AWS DNS and metadata are not filtered by security groups; this is not an air gap.")
		if e.AWS.DesktopProfile != "" {
			p.Notices = append(p.Notices, "DCV desktop machines receive a license-only instance role. This lab allows HTTPS to S3 through an endpoint restricted to the regional DCV licensing bucket.")
		}
	}
	return p, nil
}
func sortedKeys[T any](m map[string]T) []string {
	k := make([]string, 0, len(m))
	for s := range m {
		k = append(k, s)
	}
	sort.Strings(k)
	return k
}

// ProxmoxSubnet assigns disjoint /24 segments from the operator's address pool.
func ProxmoxSubnet(p ProxmoxConfig, vlan int) (string, string, error) {
	prefix, err := netip.ParsePrefix(p.CIDR)
	if err != nil {
		return "", "", err
	}
	if prefix.Bits() > 24 {
		return "", "", fmt.Errorf("Proxmox address pool must be /24 or larger")
	}
	offset := vlan - p.VLANStart
	if offset < 0 || vlan > p.VLANEnd || offset >= (1<<(24-prefix.Bits())) {
		return "", "", fmt.Errorf("VLAN does not fit address pool")
	}
	a := prefix.Masked().Addr().As4()
	n := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	n += uint32(offset * 256)
	addr := netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
	gw := ""
	if p.Gateway != "" {
		g, err := netip.ParseAddr(p.Gateway)
		if err != nil || !prefix.Contains(g) {
			return "", "", fmt.Errorf("gateway must belong to address pool")
		}
		ga := g.As4()
		gn := uint32(ga[0])<<24 | uint32(ga[1])<<16 | uint32(ga[2])<<8 | uint32(ga[3])
		gn += uint32(offset * 256)
		gw = netip.AddrFrom4([4]byte{byte(gn >> 24), byte(gn >> 16), byte(gn >> 8), byte(gn)}).String()
	}
	return netip.PrefixFrom(addr, 24).String(), gw, nil
}
