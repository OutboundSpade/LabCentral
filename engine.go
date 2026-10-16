package labcentral

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"github.com/OutboundSpade/LabCentral/v2/internal/privatefile"
	"github.com/OutboundSpade/LabCentral/v2/internal/store"
	"golang.org/x/crypto/ssh"
)

type Engine struct {
	state    *store.Store
	provider Provider
	observer Observer
	options  EngineOptions
}

func Open(root string, p Provider, o Observer) (*Engine, error) {
	return OpenWithOptions(root, p, o, EngineOptions{})
}

// OpenWithOptions opens an engine with bounded per-operation native concurrency.
// Open preserves the default limits for existing callers.
func OpenWithOptions(root string, p Provider, o Observer, options EngineOptions) (*Engine, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("provider is required")
	}
	s, e := store.Open(root)
	if e != nil {
		return nil, e
	}
	// Progress consumers need not be concurrency-safe.
	var observerMu sync.Mutex
	var observer Observer
	if o != nil {
		observer = func(event Event) { observerMu.Lock(); defer observerMu.Unlock(); o(event) }
	}
	return &Engine{state: s, provider: p, observer: observer, options: options.Effective()}, nil
}
func (e *Engine) Close() error { return e.state.Close() }
func (e *Engine) emit(stage, id, msg string) {
	if e.observer != nil {
		e.observer(Event{Version: 2, Stage: stage, Resource: id, Message: msg})
	}
}

// Allocation and retained image references share a short-lived state fence.
func (e *Engine) withAllocationLock(ctx context.Context, fn func() error) error {
	unlock, err := e.state.LockKeyContext(ctx, "mutation")
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
func id() string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (e *Engine) Check(ctx context.Context, env Environment) error {
	if err := ValidateEnvironment(env); err != nil {
		return err
	}
	_, err := e.execute(ctx, Command{Action: "check", Environment: env}, e.observer)
	return err
}
func (e *Engine) List(ctx context.Context) ([]Instance, error) {
	rows, err := e.state.List(ctx, "instance")
	if err != nil {
		return nil, err
	}
	out := []Instance{}
	for _, b := range rows {
		var i Instance
		if err = json.Unmarshal(b, &i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}
func (e *Engine) Inspect(ctx context.Context, instance string) (Instance, error) {
	var i Instance
	err := e.state.Get(ctx, "instance", instance, &i)
	return i, err
}
func (e *Engine) Images(ctx context.Context) ([]ImageRecord, error) {
	rows, err := e.state.List(ctx, "image")
	if err != nil {
		return nil, err
	}
	out := []ImageRecord{}
	for _, b := range rows {
		var i ImageRecord
		if err = json.Unmarshal(b, &i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}
func (e *Engine) Build(ctx context.Context, p Plan) ([]ImageRecord, error) {
	return e.build(ctx, p, false)
}

// BuildWithRecovery builds or reuses each image, reconciling retained attempts first.
// Only confirmed-absent attempts are abandoned and replaced. The cache-key lock
// spans reconciliation, abandonment and the new attempt across clients/processes.
// Unknown outcomes retain their original intent and identifiers for another retry.
func (e *Engine) BuildWithRecovery(ctx context.Context, p Plan) ([]ImageRecord, error) {
	return e.build(ctx, p, true)
}

func (e *Engine) lockImages(ctx context.Context, p Plan) (func(), error) {
	keys := []string{}
	for _, im := range p.Images {
		if im.BuildProgram() != "" {
			keys = append(keys, im.CacheKey)
		}
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	var releases []func()
	release := func() {
		for n := len(releases) - 1; n >= 0; n-- {
			releases[n]()
		}
	}
	for _, key := range keys {
		unlock, err := e.state.LockKeyContext(ctx, "image:"+key)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, unlock)
	}
	return release, nil
}
func verifyPlan(p Plan) error {
	fresh, err := PlanLab(p.Source, p.Environment, inputMap(p.Inputs))
	if err != nil {
		return err
	}
	if digest(fresh) != digest(p) {
		return problem(Invalid, "plan", "plan changed after validation")
	}
	return nil
}
func inputMap(in []InputValue) map[string]string {
	m := map[string]string{}
	for _, v := range in {
		m[v.Name] = v.Value
	}
	return m
}
func (e *Engine) build(ctx context.Context, p Plan, recoverFailed bool) ([]ImageRecord, error) {
	if err := verifyPlan(p); err != nil {
		return nil, err
	}
	if _, err := e.execute(ctx, Command{Action: "check", Environment: p.Environment, Source: p.Source}, e.observer); err != nil {
		return nil, err
	}
	// Images have no dependencies on one another. A cache-key fence preserves
	// single ownership across processes; native work never holds the allocation fence.
	results := make([][]ImageRecord, len(p.Images))
	failures := make([]error, len(p.Images))
	slots := make(chan struct{}, e.options.MaxImageBuildConcurrency)
	var workers sync.WaitGroup
	for n, im := range p.Images {
		if im.BuildProgram() == "" {
			continue
		}
		workers.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				if err := ctx.Err(); err != nil {
					failures[n] = err
					return
				}
			case <-ctx.Done():
				failures[n] = ctx.Err()
				return
			}
			results[n], failures[n] = e.buildImageWithRecovery(ctx, p, im, recoverFailed)
		})
	}
	workers.Wait()
	var images []ImageRecord
	for _, result := range results {
		images = append(images, result...)
	}
	return images, errors.Join(failures...)
}

func (e *Engine) buildImage(ctx context.Context, p Plan, im PlannedImage) ([]ImageRecord, error) {
	return e.buildImageWithRecovery(ctx, p, im, false)
}

func (e *Engine) buildImageWithRecovery(ctx context.Context, p Plan, im PlannedImage, recoverFailed bool) ([]ImageRecord, error) {
	ctx = faultinject.WithOperation(ctx, p.Environment.Platform, "image.build")
	unlock, err := e.state.LockKeyContext(ctx, "image:"+im.CacheKey)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return e.buildImageLocked(ctx, p, im, recoverFailed)
}

// Caller holds the image's cache-key lock, including through a replacement attempt.
func (e *Engine) buildImageLocked(ctx context.Context, p Plan, im PlannedImage, recoverFailed bool) ([]ImageRecord, error) {
	var cleanup Command
	if err := e.state.Get(ctx, "image-cleanup", im.CacheKey, &cleanup); err == nil {
		if !recoverFailed || cleanup.Action != "image.abandon" {
			return nil, problem(RecoveryRequired, im.CacheKey, "image cleanup is pending; confirm removal before rebuilding")
		}
		// Resume durable abandonment after an interrupted retry. This cannot be a
		// published-image removal; abandonBuildLocked checks publication again.
		if err := e.abandonBuildLocked(ctx, im.CacheKey); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	var images []ImageRecord
	var cached ImageRecord
	err := e.state.Get(ctx, "image", im.CacheKey, &cached)
	if err == nil {
		r, er := e.execute(ctx, Command{Action: "image.read", Owner: cached.Resource.Owner, Environment: p.Environment, Prior: cached.Resource}, e.observer)
		if er != nil {
			return nil, er
		}
		if !r.Absent && r.Resource.Complete {
			return []ImageRecord{cached}, nil
		}
		return nil, problem(RecoveryRequired, im.Name, "cached native image is absent or changed; remove its record explicitly")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var cmd Command
	err = e.state.Get(ctx, "build", im.CacheKey, &cmd)
	pending := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		owner := "build-" + id()
		cmd = Command{Action: "image.build", Operation: owner, Owner: owner, Environment: p.Environment, Image: im, Source: p.Source, Inputs: p.Inputs}
		if err = e.state.Put(ctx, "build", im.CacheKey, cmd); err != nil {
			return images, err
		}
	} else if err != nil {
		return images, err
	}
	owner := cmd.Owner
	if p.Environment.Platform == "proxmox" {
		v, err := e.state.Reserve(ctx, "vmid:"+p.Environment.Proxmox.URL, owner, p.Environment.Proxmox.VMIDStart, p.Environment.Proxmox.VMIDEnd, 3)
		if err != nil {
			return images, err
		}
		cmd.Machine.VMID = v[0]
		cmd.ValidationVMID = v[1]
		cmd.GatewayVMID = v[2]
		cmd.Gateway.Address, err = e.reserveProxmoxManagement(ctx, cmd.Environment.Proxmox, owner)
		if err != nil {
			return images, err
		}
		vlans, err := e.state.Reserve(ctx, "vlan:"+p.Environment.Proxmox.URL+":"+p.Environment.Proxmox.LabBridge, owner, p.Environment.Proxmox.VLANStart, p.Environment.Proxmox.VLANEnd, 1)
		if err != nil {
			return images, err
		}
		cmd.VLAN = vlans[0]
		if err = e.state.Put(ctx, "build", im.CacheKey, cmd); err != nil {
			return images, err
		}
	}
	r, err := e.invoke(ctx, cmd)
	if err != nil {
		return images, err
	}
	if r.Absent {
		if recoverFailed && pending {
			if err := e.abandonBuildLocked(ctx, im.CacheKey); err != nil {
				return images, err
			}
			return e.buildImageLocked(ctx, p, im, false)
		}
		return images, problem(RecoveryRequired, im.Name, "incomplete build resources were cleaned; abandon the failed attempt before rebuilding")
	}
	if !r.Resource.Complete {
		return images, problem(RecoveryRequired, im.Name, "build is incomplete")
	}
	rec := ImageRecord{ID: owner, Key: im.CacheKey, Name: im.Name, EnvironmentDigest: p.EnvironmentDigest, Resource: r.Resource, Created: time.Now().UTC(), Environment: p.Environment}
	_, err = faultinject.Call(faultinject.WithOperation(ctx, p.Environment.Platform, "image.build"), "publish", func(ctx context.Context) (struct{}, error) {
		// A completed library/provider intent can survive a crash before publication.
		// Its cached result does not prove that the capture still exists or is owned.
		current, err := e.execute(ctx, Command{Action: "image.read", Owner: owner, Environment: p.Environment, Prior: r.Resource}, e.observer)
		if err != nil {
			return struct{}{}, fmt.Errorf("confirm captured image before publication: %w", err)
		}
		if current.Absent || !current.Resource.Complete || current.Resource.ID != r.Resource.ID || current.Resource.Owner != owner {
			return struct{}{}, problem(RecoveryRequired, im.Name, "captured image ownership or availability is unconfirmed; inspect the native image before recovery")
		}
		return struct{}{}, e.publishImage(ctx, im.CacheKey, rec, cmd)
	})
	if err != nil {
		return images, err
	}
	images = append(images, rec)
	return images, nil
}

// Publication and release of disposable qualification reservations form one commit.
// A crash must not publish an image while stranding its auxiliary allocations.
func (e *Engine) publishImage(ctx context.Context, key string, rec ImageRecord, cmd Command) error {
	tx, err := e.state.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO objects VALUES('image',?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body`, key, body); err != nil {
		return err
	}
	if cmd.Environment.Platform == "proxmox" {
		p := cmd.Environment.Proxmox
		if _, err = tx.ExecContext(ctx, `DELETE FROM claims WHERE owner=? AND ((scope=? AND value IN (?,?)) OR (scope=? AND value=?))`, cmd.Owner, "vmid:"+p.URL, cmd.ValidationVMID, cmd.GatewayVMID, "vlan:"+p.URL+":"+p.LabBridge, cmd.VLAN); err != nil {
			return err
		}
		if p.ManagementAddresses != nil {
			if _, err = tx.ExecContext(ctx, `DELETE FROM claims WHERE owner=? AND scope=?`, cmd.Owner, managementScope(p)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (e *Engine) Deploy(ctx context.Context, p Plan) (Instance, error) {
	return e.DeployWithID(ctx, p, id())
}

// DeployWithID lets durable callers reserve the instance identity before dispatch.
// Existing identities require Recover rather than starting a replacement.
func (e *Engine) DeployWithID(ctx context.Context, p Plan, instanceID string) (Instance, error) {
	var inst Instance
	if !regexp.MustCompile(`^[a-f0-9]{24}$`).MatchString(instanceID) {
		return inst, problem(Invalid, "instance", "expected a 24-character hexadecimal identity")
	}
	unlock, err := e.state.LockKeyContext(ctx, "instance:"+instanceID)
	if err != nil {
		return inst, err
	}
	defer unlock()
	// The instance fence makes this preflight authoritative. Never create images
	// for a duplicate identity that must resume its retained original plan.
	if err := e.state.Get(ctx, "instance", instanceID, &inst); err == nil {
		return inst, problem(RecoveryRequired, "instance", "instance already exists; recover its original operation")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return inst, err
	}
	images, err := e.Build(ctx, p)
	if err != nil {
		return inst, err
	}
	unpin, err := e.lockImages(ctx, p)
	if err != nil {
		return inst, err
	}
	defer func() { unpin() }()
	if err := e.prepareCredentials(instanceID, p.Machines); err != nil {
		return inst, err
	}
	if p.Environment.Platform == "aws" {
		if _, err := ensureKey(filepath.Join(e.state.Root, "credentials", instanceID, ".gateway.host")); err != nil {
			return inst, err
		}
	}
	err = e.withAllocationLock(ctx, func() error {
		if err := e.state.Get(ctx, "instance", instanceID, &inst); err == nil {
			return problem(RecoveryRequired, "instance", "instance already exists; recover its original operation")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := verifyPlan(p); err != nil {
			return err
		}
		// Removal may have won the fence after Build returned. Never pin an absent record.
		for _, im := range images {
			var current ImageRecord
			if err := e.state.Get(ctx, "image", im.Key, &current); err != nil {
				return err
			}
			if current.Resource.ID != im.Resource.ID {
				return problem(RecoveryRequired, im.Key, "image changed before deployment")
			}
			// An unconfirmed removal keeps the record; never deploy from it.
			var cleanup Command
			if err := e.state.Get(ctx, "image-cleanup", im.Key, &cleanup); err == nil {
				return problem(RecoveryRequired, im.Key, "image cleanup is pending; confirm removal before deploying")
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		native := map[string]string{}
		for _, im := range p.Images {
			if im.BuildProgram() == "" {
				native[im.Name] = im.Base.ID
			}
		}
		for _, im := range images {
			native[im.Name] = im.Resource.ID
		}
		// Native images, addresses and guest identities belong to this deployment.
		// A caller can reuse the validated plan for other independent instances.
		p.Machines = slices.Clone(p.Machines)
		inst = Instance{ID: instanceID, Name: p.Source.Lab.Name, Status: "preparing", Created: time.Now().UTC(), Plan: p}
		if p.Environment.Platform == "aws" {
			g := p.Environment.AWS.Gateway.Effective()
			b := p.Environment.Bases[g.Base]
			inst.Gateway = &PlannedMachine{Name: "gateway", NativeImage: b.ID, InstanceType: g.InstanceType, DiskGiB: b.DiskGiB}
		}
		if p.Environment.Platform == "proxmox" {
			b := p.Environment.Bases[p.Environment.Proxmox.EffectiveGatewayBase()]
			inst.Gateway = &PlannedMachine{Name: "gateway", NativeImage: b.ID, CPU: 1, MemoryMiB: 768, DiskGiB: b.DiskGiB}
		}
		for n := range inst.Plan.Machines {
			inst.Plan.Machines[n].NativeImage = native[inst.Plan.Machines[n].Spec.Image]
		}
		if err = e.state.Put(ctx, "instance", inst.ID, inst); err != nil {
			return err
		}
		if err = e.prepare(ctx, &inst); err != nil {
			return e.failed(inst, err)
		}
		return nil
	})
	unpin()
	unpin = func() {}
	if err == nil {
		err = e.deploy(ctx, &inst)
	}
	return inst, err
}
func (e *Engine) prepare(ctx context.Context, i *Instance) error {
	env := i.Plan.Environment
	ctx = faultinject.WithOperation(ctx, env.Platform, "deploy")
	legacy := len(i.Plan.Networks) == 0
	var vlans []int
	if env.Platform == "proxmox" {
		p := env.Proxmox
		v, err := e.state.Reserve(ctx, "vlan:"+p.URL+":"+p.LabBridge, i.ID, p.VLANStart, p.VLANEnd, len(effectiveNetworks(i.Plan.Source.Lab)))
		if err != nil {
			return err
		}
		i.VLAN = v[0]
		vlans = v
		ids, err := e.state.Reserve(ctx, "vmid:"+p.URL, i.ID, p.VMIDStart, p.VMIDEnd, len(i.Plan.Machines)+boolInt(i.Gateway != nil))
		if err != nil {
			return err
		}
		for n := range i.Plan.Machines {
			i.Plan.Machines[n].VMID = ids[n]
		}
		if i.Gateway != nil {
			i.Gateway.VMID = ids[len(ids)-1]
			if p.ManagementAddresses != nil {
				i.Gateway.Address, err = e.reserveProxmoxManagement(ctx, p, i.ID)
				if err != nil {
					return err
				}
			}
		}
	}
	if err := PlanNetworks(&i.Plan, vlans); err != nil {
		return err
	}
	if legacy { // Retained pre-Phase-3 operations keep their native request shape.
		i.Plan.Networks = nil
		i.Plan.VPCCIDR, i.Plan.EdgeCIDR = "", ""
		for j := range i.Plan.Machines {
			i.Plan.Machines[j].NICs = nil
			i.Plan.Machines[j].Resolver = ""
		}
	}
	keyDir := filepath.Join(e.state.Root, "credentials", i.ID)
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return err
	}
	pub, err := ensureKey(filepath.Join(keyDir, "id_ed25519"))
	if err != nil {
		return err
	}
	if i.Gateway != nil {
		host, err := ensureKey(filepath.Join(keyDir, ".gateway.host"))
		if err != nil {
			return err
		}
		i.Gateway.PublicKey, i.Gateway.HostPublicKey = pub, host
	}
	for n := range i.Plan.Machines {
		m := &i.Plan.Machines[n]
		m.PublicKey = pub
		host, err := ensureKey(filepath.Join(keyDir, m.Name+".host"))
		if err != nil {
			return err
		}
		m.HostPublicKey = host
	}
	return e.state.Put(ctx, "instance", i.ID, i)
}
func ensureKey(path string) (string, error) {
	if b, e := os.ReadFile(path); e == nil {
		key, e := ssh.ParsePrivateKey(b)
		if e != nil {
			return "", e
		}
		return string(ssh.MarshalAuthorizedKey(key.PublicKey())), nil
	} else if !os.IsNotExist(e) {
		return "", e
	}
	_, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return "", e
	}
	block, e := ssh.MarshalPrivateKey(priv, "")
	if e != nil {
		return "", e
	}
	if e = privatefile.Write(path, pem.EncodeToMemory(block)); e != nil {
		return "", e
	}
	pub, _ := ssh.NewPublicKey(priv.Public())
	return string(ssh.MarshalAuthorizedKey(pub)), nil
}

// execute covers a lost provider result before the library can commit it.
func (e *Engine) execute(ctx context.Context, c Command, o Observer) (Result, error) {
	ctx = faultinject.WithOperation(ctx, c.Environment.Platform, c.Action)
	return faultinject.Call(ctx, "provider.result", func(ctx context.Context) (Result, error) { return e.provider.Execute(ctx, c, o) })
}

func (e *Engine) invoke(ctx context.Context, c Command) (Result, error) {
	st, err := e.state.Intent(ctx, c.Operation, c.Owner, c)
	if err != nil {
		return Result{}, err
	}
	if st.Status == "complete" {
		var r Result
		err = json.Unmarshal(st.Result, &r)
		return r, err
	}
	if st.Status == "uncertain" {
		c.Recovery = true
	}
	if err = e.state.Started(ctx, c.Operation); err != nil {
		return Result{}, err
	}
	if c.Action == "machine.create" || c.Action == "gateway.create" {
		hostFile := c.Machine.Name + ".host"
		if c.Action == "gateway.create" {
			hostFile = ".gateway.host"
		}
		b, er := os.ReadFile(filepath.Join(e.state.Root, "credentials", c.Owner, hostFile))
		if er != nil {
			return Result{}, er
		}
		c.Machine.HostPrivateKey = string(b)
		c.ClientPrivateKey, er = os.ReadFile(filepath.Join(e.state.Root, "credentials", c.Owner, "id_ed25519"))
		if er != nil {
			return Result{}, er
		}
	}
	e.emit(c.Action, c.Owner, "")
	r, err := e.execute(ctx, c, e.observer)
	if err != nil {
		return r, err
	}
	if (c.Action == "machine.create" || c.Action == "gateway.create" || c.Action == "network.create") && !r.Resource.Complete {
		return r, problem(RecoveryRequired, c.Operation, "resource has not reached ready")
	}
	if (c.Action == "resource.destroy" || c.Action == "image.abandon") && !r.Absent {
		return r, problem(RecoveryRequired, c.Operation, "deletion is not confirmed")
	}
	if err = e.state.Complete(ctx, c.Operation, r); err != nil {
		return r, err
	}
	return r, nil
}
func (e *Engine) deploy(ctx context.Context, i *Instance) error {
	env := i.Plan.Environment
	net, err := e.invoke(ctx, Command{Action: "network.create", Operation: i.ID + "/network", Owner: i.ID, Environment: env, Source: i.Plan.Source, VLAN: i.VLAN, Networks: i.Plan.Networks})
	if err != nil {
		return e.failed(*i, err)
	}
	i.Resources = upsert(i.Resources, net.Resource)
	if err = e.state.Put(ctx, "instance", i.ID, i); err != nil {
		return err
	}
	gateway := Resource{}
	if i.Gateway != nil {
		result, err := e.invoke(ctx, Command{Action: "gateway.create", Operation: i.ID + "/gateway", Owner: i.ID, Environment: env, Source: i.Plan.Source, Networks: i.Plan.Networks, Machine: *i.Gateway, Prior: Resource{Kind: "gateway"}, Network: net.Resource})
		if err != nil {
			return e.failed(*i, err)
		}
		gateway = result.Resource
		i.Resources = upsert(i.Resources, gateway)
		if err = e.state.Put(ctx, "instance", i.ID, i); err != nil {
			return err
		}
	}
	// Networking and the optional gateway precede machines. Persist each sibling
	// independently and drain all work before recording a partial failure.
	var mu sync.Mutex
	var workers sync.WaitGroup
	slots := make(chan struct{}, e.options.MaxMachineConcurrency)
	failures := make([]error, len(i.Plan.Machines))
	for n, m := range i.Plan.Machines {
		workers.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				if err := ctx.Err(); err != nil {
					failures[n] = err
					return
				}
			case <-ctx.Done():
				failures[n] = ctx.Err()
				return
			}
			r, err := e.invoke(ctx, Command{Action: "machine.create", Operation: i.ID + "/machine/" + m.Name, Owner: i.ID, Environment: env, Machine: m, Network: net.Resource, Gateway: gateway, Source: i.Plan.Source, VLAN: i.VLAN, Networks: i.Plan.Networks})
			if err != nil {
				failures[n] = fmt.Errorf("%s: %w", m.Name, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			i.Resources = upsert(i.Resources, r.Resource)
			failures[n] = e.state.Put(ctx, "instance", i.ID, i)
		})
	}
	workers.Wait()
	if err := errors.Join(failures...); err != nil {
		return e.failed(*i, err)
	}
	i.Status = "ready"
	i.Error = ""
	return e.state.Put(ctx, "instance", i.ID, i)
}
func upsert(rs []Resource, r Resource) []Resource {
	for n, v := range rs {
		if v.Kind == r.Kind && v.Name == r.Name {
			rs[n] = r
			return rs
		}
	}
	return append(rs, r)
}
func (e *Engine) failed(i Instance, cause error) error {
	i.Status = "recovery_required"
	i.Error = cause.Error()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.state.Put(ctx, "instance", i.ID, i); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
func (e *Engine) Recover(ctx context.Context, instance string) (Instance, error) {
	unlock, err := e.state.LockKey("instance:" + instance)
	if err != nil {
		return Instance{}, problem(Busy, "instance", err.Error())
	}
	defer unlock()
	var i Instance
	i, err = e.Inspect(ctx, instance)
	if err != nil {
		return i, err
	}
	if i.Status != "destroyed" && i.Status != "destroying" {
		if err = e.closeInstanceAccess(ctx, i.ID); err != nil {
			return i, err
		}
	}
	if i.Status != "destroyed" && i.Status != "destroying" {
		if err := e.prepareCredentials(i.ID, i.Plan.Machines); err != nil {
			return i, err
		}
		if i.Gateway != nil {
			if _, err := ensureKey(filepath.Join(e.state.Root, "credentials", i.ID, ".gateway.host")); err != nil {
				return i, err
			}
		}
	}
	err = e.withAllocationLock(ctx, func() error {
		var err error
		i, err = e.Inspect(ctx, instance)
		if err != nil {
			return err
		}
		if i.Status == "destroyed" {
			return nil
		}
		if i.Status == "destroying" {
			return nil
		}
		if i.Status != "ready" {
			if err = e.prepare(ctx, &i); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && i.Status == "destroying" {
		err = e.destroy(ctx, &i)
	} else if err == nil && i.Status != "destroyed" {
		err = e.deploy(ctx, &i)
	}
	return i, err
}
func (e *Engine) Destroy(ctx context.Context, instance string) error {
	unlock, err := e.state.LockKeyContext(ctx, "instance:"+instance)
	if err != nil {
		return problem(Busy, "instance", err.Error())
	}
	defer unlock()
	i, err := e.Inspect(ctx, instance)
	if err != nil {
		return err
	}
	return e.destroy(ctx, &i)
}
func (e *Engine) destroy(ctx context.Context, i *Instance) error {
	if i.Status == "destroyed" {
		return nil
	}
	// Guest cleanup is best effort before termination. Native VM absence also
	// revokes its sessions; keep access intent until that absence is confirmed.
	_ = e.closeInstanceAccess(ctx, i.ID)
	i.Status = "destroying"
	if err := e.state.Put(ctx, "instance", i.ID, i); err != nil {
		return err
	}
	steps, err := e.state.Uncertain(ctx, i.ID)
	if err != nil {
		return err
	}
	for _, st := range steps {
		var cmd Command
		if err = json.Unmarshal(st.Command, &cmd); err != nil {
			return err
		}
		if cmd.Action == "resource.destroy" {
			continue
		}
		cmd.Action = "discover"
		cmd.Recovery = true
		r, err := e.execute(ctx, cmd, e.observer)
		if err != nil {
			return err
		}
		if r.Absent {
			return problem(RecoveryRequired, st.ID, "absence after an uncertain create is not proof; retry discovery")
		}
		i.Resources = upsert(i.Resources, r.Resource)
		if err = e.state.Put(ctx, "instance", i.ID, i); err != nil {
			return err
		}
		if err = e.state.Complete(ctx, st.ID, r); err != nil {
			return err
		}
	}
	if err = e.state.Put(ctx, "instance", i.ID, i); err != nil {
		return err
	}
	completed, err := e.state.CompletedSteps(ctx, i.ID)
	if err != nil {
		return err
	}
	for _, step := range completed {
		var result Result
		if err = json.Unmarshal(step.Result, &result); err != nil {
			return err
		}
		r := result.Resource
		if r.Kind == "machine" || r.Kind == "gateway" || r.Kind == "network" {
			if r.Owner != i.ID {
				return problem(OwnershipMismatch, step.ID, "completed resource owner changed")
			}
			i.Resources = upsert(i.Resources, r)
		}
	}
	if err = e.state.Put(ctx, "instance", i.ID, i); err != nil {
		return err
	}
	// Machines share only their network. Finish every machine deletion before
	// touching dependencies; keep successful steps durable when a sibling fails.
	destroy := func(r Resource) error {
		_, err := e.invoke(ctx, Command{Action: "resource.destroy", Operation: i.ID + "/destroy/" + r.Kind + "/" + r.Name, Owner: i.ID, Environment: i.Plan.Environment, Prior: r})
		return err
	}
	var failures []error
	var mu sync.Mutex
	var workers sync.WaitGroup
	slots := make(chan struct{}, e.options.MaxCleanupConcurrency)
	for _, r := range i.Resources {
		if r.Kind != "machine" {
			continue
		}
		workers.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				if err := ctx.Err(); err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
					return
				}
			case <-ctx.Done():
				mu.Lock()
				failures = append(failures, ctx.Err())
				mu.Unlock()
				return
			}
			if err := destroy(r); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		})
	}
	workers.Wait()
	if err := errors.Join(failures...); err != nil {
		return err
	}
	for _, kind := range []string{"gateway", "network"} {
		for _, r := range i.Resources {
			if r.Kind == kind {
				if err := destroy(r); err != nil {
					return err
				}
			}
		}
	}
	records, err := e.state.List(ctx, "access")
	if err != nil {
		return err
	}
	for _, b := range records {
		var c Command
		if err = json.Unmarshal(b, &c); err != nil {
			return err
		}
		if c.Owner == i.ID {
			if err = e.state.Delete(ctx, "access", c.Operation); err != nil {
				return err
			}
		}
	}
	if err = e.state.Release(ctx, i.ID); err != nil {
		return err
	}
	i.Status = "destroyed"
	i.Error = ""
	return e.state.Put(ctx, "instance", i.ID, i)
}
func (e *Engine) RemoveImage(ctx context.Context, key string) error {
	unlock, err := e.state.LockKeyContext(ctx, "image:"+key)
	if err != nil {
		return err
	}
	defer unlock()
	var im ImageRecord
	var cmd Command
	err = e.withAllocationLock(ctx, func() error {
		if err := e.state.Get(ctx, "image", key, &im); err != nil {
			return err
		}
		// Check exact native identities and target scope without decoding every
		// retained source snapshot while holding the allocation fence.
		var referenced bool
		err := e.state.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM objects instance,json_each(instance.body,'$.plan.machines') machine WHERE instance.kind='instance' AND coalesce(json_extract(instance.body,'$.status'),'')!='destroyed' AND json_extract(machine.value,'$.nativeImage')=? AND json_extract(instance.body,'$.plan.environment.platform')=? AND CASE WHEN ?='aws' THEN json_extract(instance.body,'$.plan.environment.aws.account')=? AND json_extract(instance.body,'$.plan.environment.aws.region')=? ELSE json_extract(instance.body,'$.plan.environment.proxmox.url')=? END)`, im.Resource.ID, im.Environment.Platform, im.Environment.Platform, im.Environment.AWS.Account, im.Environment.AWS.Region, im.Environment.Proxmox.URL).Scan(&referenced)
		if err != nil {
			return err
		}
		if referenced {
			return problem(Invalid, key, "image is referenced by a retained instance")
		}
		cmd = Command{Action: "resource.destroy", Operation: im.ID + "/remove", Owner: im.Resource.Owner, Environment: im.Environment, Prior: im.Resource}
		return e.state.Put(ctx, "image-cleanup", key, cmd)
	})
	if err != nil {
		return err
	}
	r, err := e.invoke(ctx, cmd)
	if err != nil {
		return err
	}
	if !r.Absent {
		return problem(RecoveryRequired, key, "image deletion unconfirmed")
	}
	return e.withAllocationLock(ctx, func() error { return e.finishImageCleanup(ctx, key, im.ID, true) })
}

// WithSSHMaterial holds the machine lock and shared instance lifecycle guard for a
// bounded caller-owned SSH operation. Credentials must stay local.
func (e *Engine) WithSSHMaterial(ctx context.Context, instance, machine string, fn func(Instance, PlannedMachine, Resource, []byte) error) error {
	return e.withMachineLock(ctx, instance, machine, func() error {
		return e.useSSHMaterial(ctx, instance, machine, fn)
	})
}

// WithSSHSession holds the shared instance lifecycle guard for an interactive
// SSH session. It permits concurrent machine operations, including guest steps;
// callers must bound and close their session before returning. Credentials stay
// local. Use WithSSHMaterial for operations that need machine serialization.
func (e *Engine) WithSSHSession(ctx context.Context, instance, machine string, fn func(Instance, PlannedMachine, Resource, []byte) error) error {
	unlock, err := e.state.LockKeySharedContext(ctx, "instance:"+instance)
	if err != nil {
		return problem(Busy, "instance", err.Error())
	}
	defer unlock()
	return e.useSSHMaterial(ctx, instance, machine, fn)
}

func (e *Engine) useSSHMaterial(ctx context.Context, instance, machine string, fn func(Instance, PlannedMachine, Resource, []byte) error) error {
	i, m, r, key, err := e.SSHMaterial(ctx, instance, machine)
	if err != nil {
		return err
	}
	if !Contains(m.Spec.Access, "ssh") {
		return problem(Unsupported, machine, "SSH access is not declared")
	}
	return fn(i, m, r, key)
}

// SSHMaterial returns credentials only through an explicit access request.
func (e *Engine) SSHMaterial(ctx context.Context, instance, machine string) (Instance, PlannedMachine, Resource, []byte, error) {
	i, err := e.Inspect(ctx, instance)
	if err != nil {
		return i, PlannedMachine{}, Resource{}, nil, err
	}
	if i.Status != "ready" {
		return i, PlannedMachine{}, Resource{}, nil, problem(Invalid, instance, "instance is not ready")
	}
	for _, m := range i.Plan.Machines {
		if m.Name == machine {
			for _, r := range i.Resources {
				if r.Kind == "machine" && r.Name == machine {
					b, err := os.ReadFile(filepath.Join(e.state.Root, "credentials", instance, "id_ed25519"))
					return i, m, r, b, err
				}
			}
		}
	}
	return i, PlannedMachine{}, Resource{}, nil, fmt.Errorf("machine not found")
}

// Builds lists unpublished and retained build intents, including interrupted attempts.
func (e *Engine) Builds(ctx context.Context) ([]Command, error) {
	rows, err := e.state.List(ctx, "build")
	if err != nil {
		return nil, err
	}
	out := []Command{}
	for _, b := range rows {
		var c Command
		if err = json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// RecoverBuild resumes the original source and environment snapshot.
func (e *Engine) RecoverBuild(ctx context.Context, key string) ([]ImageRecord, error) {
	var c Command
	if err := e.state.Get(ctx, "build", key, &c); err != nil {
		return nil, err
	}
	p, err := PlanLab(c.Source, c.Environment, inputMap(c.Inputs))
	if err != nil {
		return nil, err
	}
	return e.Build(ctx, p)
}

// AbandonBuild explicitly cleans an unpublished attempt before releasing identifiers.
func (e *Engine) AbandonBuild(ctx context.Context, key string) error {
	unlock, err := e.state.LockKeyContext(ctx, "image:"+key)
	if err != nil {
		return err
	}
	defer unlock()
	return e.abandonBuildLocked(ctx, key)
}

func (e *Engine) abandonBuildLocked(ctx context.Context, key string) error {
	var c Command
	err := e.withAllocationLock(ctx, func() error {
		var im ImageRecord
		if err := e.state.Get(ctx, "image", key, &im); err == nil {
			return problem(Invalid, key, "published image: use image remove")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := e.state.Get(ctx, "build", key, &c); err != nil {
			return err
		}
		c.Action = "image.abandon"
		c.Operation = c.Owner + "/abandon"
		return e.state.Put(ctx, "image-cleanup", key, c)
	})
	if err != nil {
		return err
	}
	r, err := e.invoke(ctx, c)
	if err != nil {
		return err
	}
	if !r.Absent {
		return problem(RecoveryRequired, key, "build cleanup unconfirmed")
	}
	return e.withAllocationLock(ctx, func() error { return e.finishImageCleanup(ctx, key, c.Owner, false) })
}

func sameTarget(a, b Environment) bool {
	if a.Platform != b.Platform {
		return false
	}
	if a.Platform == "aws" {
		return a.AWS.Account == b.AWS.Account && a.AWS.Region == b.AWS.Region
	}
	return a.Proxmox.URL == b.Proxmox.URL
}

// Key generation is protected by the instance lifecycle guard, independent of
// allocation namespaces and image reference decisions.
func (e *Engine) prepareCredentials(instance string, machines []PlannedMachine) error {
	dir := filepath.Join(e.state.Root, "credentials", instance)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if _, err := ensureKey(filepath.Join(dir, "id_ed25519")); err != nil {
		return err
	}
	for _, m := range machines {
		if _, err := ensureKey(filepath.Join(dir, m.Name+".host")); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) finishImageCleanup(ctx context.Context, key, owner string, published bool) error {
	tx, err := e.state.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM claims WHERE owner=?`, owner); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM objects WHERE id=? AND kind IN ('build','image-cleanup')`, key); err != nil {
		return err
	}
	if published {
		if _, err = tx.ExecContext(ctx, `DELETE FROM objects WHERE kind='image' AND id=?`, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func managementScope(p ProxmoxConfig) string {
	return "management:" + p.URL + ":" + p.ManagementBridge + ":" + p.ManagementAddresses.CIDR
}
func (e *Engine) reserveProxmoxManagement(ctx context.Context, p ProxmoxConfig, owner string) (string, error) {
	if p.ManagementAddresses == nil {
		return "", nil
	}
	prefix := netip.MustParsePrefix(p.ManagementAddresses.CIDR)
	limit := (1 << (32 - prefix.Bits())) - 2
	offsets, err := e.state.Reserve(ctx, managementScope(p), owner, 4, limit, 1)
	if err != nil {
		return "", err
	}
	return offsetAddress(prefix, offsets[0]), nil
}
