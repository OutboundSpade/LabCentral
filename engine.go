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
	_, err := e.provider.Execute(ctx, Command{Action: "check", Environment: env}, e.observer)
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
	return e.build(ctx, p)
}

func (e *Engine) lockImages(ctx context.Context, p Plan) (func(), error) {
	keys := []string{}
	for _, im := range p.Images {
		if im.Playbook != "" {
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
func (e *Engine) build(ctx context.Context, p Plan) ([]ImageRecord, error) {
	if err := verifyPlan(p); err != nil {
		return nil, err
	}
	if err := e.Check(ctx, p.Environment); err != nil {
		return nil, err
	}
	// Images have no dependencies on one another. A cache-key fence preserves
	// single ownership across processes; native work never holds the allocation fence.
	results := make([][]ImageRecord, len(p.Images))
	failures := make([]error, len(p.Images))
	slots := make(chan struct{}, e.options.MaxImageBuildConcurrency)
	var workers sync.WaitGroup
	for n, im := range p.Images {
		if im.Playbook == "" {
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
			results[n], failures[n] = e.buildImage(ctx, p, im)
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
	unlock, err := e.state.LockKeyContext(ctx, "image:"+im.CacheKey)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var images []ImageRecord
	var cached ImageRecord
	err = e.state.Get(ctx, "image", im.CacheKey, &cached)
	if err == nil {
		r, er := e.provider.Execute(ctx, Command{Action: "image.read", Owner: cached.Resource.Owner, Environment: p.Environment, Prior: cached.Resource}, e.observer)
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
		v, err := e.state.Reserve(ctx, "vmid:"+p.Environment.Proxmox.URL, owner, p.Environment.Proxmox.VMIDStart, p.Environment.Proxmox.VMIDEnd, 2)
		if err != nil {
			return images, err
		}
		cmd.Machine.VMID = v[0]
		cmd.ValidationVMID = v[1]
		vlans, err := e.state.Reserve(ctx, "vlan:"+p.Environment.Proxmox.URL+":"+p.Environment.Proxmox.Bridge, owner, p.Environment.Proxmox.VLANStart, p.Environment.Proxmox.VLANEnd, 1)
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
	if !r.Resource.Complete {
		return images, problem(RecoveryRequired, im.Name, "build is incomplete")
	}
	rec := ImageRecord{ID: owner, Key: im.CacheKey, Name: im.Name, EnvironmentDigest: p.EnvironmentDigest, Resource: r.Resource, Created: time.Now().UTC(), Environment: p.Environment}
	if err = e.state.Put(ctx, "image", im.CacheKey, rec); err != nil {
		return images, err
	}
	if p.Environment.Platform == "proxmox" {
		if err = e.state.ReleaseValue(ctx, "vmid:"+p.Environment.Proxmox.URL, owner, cmd.ValidationVMID); err != nil {
			return images, err
		}
		if err = e.state.ReleaseValue(ctx, "vlan:"+p.Environment.Proxmox.URL+":"+p.Environment.Proxmox.Bridge, owner, cmd.VLAN); err != nil {
			return images, err
		}
	}
	images = append(images, rec)
	return images, nil
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
		}
		native := map[string]string{}
		for _, im := range p.Images {
			if im.Playbook == "" {
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
	if env.Platform == "proxmox" {
		p := env.Proxmox
		v, err := e.state.Reserve(ctx, "vlan:"+p.URL+":"+p.Bridge, i.ID, p.VLANStart, p.VLANEnd, 1)
		if err != nil {
			return err
		}
		i.VLAN = v[0]
		ids, err := e.state.Reserve(ctx, "vmid:"+p.URL, i.ID, p.VMIDStart, p.VMIDEnd, len(i.Plan.Machines))
		if err != nil {
			return err
		}
		for n := range i.Plan.Machines {
			i.Plan.Machines[n].VMID = ids[n]
		}
	}
	prefix := "10.77.0.0/24"
	if env.Platform == "proxmox" {
		var err error
		prefix, _, err = ProxmoxSubnet(env.Proxmox, i.VLAN)
		if err != nil {
			return err
		}
	}
	p, _ := netip.ParsePrefix(prefix)
	addr := p.Masked().Addr()
	for n := 0; n < 10; n++ {
		addr = addr.Next()
	}
	keyDir := filepath.Join(e.state.Root, "credentials", i.ID)
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return err
	}
	pub, err := ensureKey(filepath.Join(keyDir, "id_ed25519"))
	if err != nil {
		return err
	}
	for n := range i.Plan.Machines {
		m := &i.Plan.Machines[n]
		m.Address = addr.String()
		addr = addr.Next()
		m.PublicKey = pub
		host, err := ensureKey(filepath.Join(keyDir, m.Name+".host"))
		if err != nil {
			return err
		}
		m.HostPublicKey = host
	}
	hosts := []HostEntry{}
	for _, m := range i.Plan.Machines {
		hosts = append(hosts, HostEntry{Name: m.Name, Address: m.Address})
	}
	for n := range i.Plan.Machines {
		m := &i.Plan.Machines[n]
		m.Hosts = []HostEntry{}
		for _, host := range hosts {
			if len(m.Spec.HostnamePeers) == 0 || Contains(m.Spec.HostnamePeers, host.Name) {
				m.Hosts = append(m.Hosts, host)
			}
		}
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
	if c.Action == "machine.create" {
		b, er := os.ReadFile(filepath.Join(e.state.Root, "credentials", c.Owner, c.Machine.Name+".host"))
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
	r, err := e.provider.Execute(ctx, c, e.observer)
	if err != nil {
		return r, err
	}
	if (c.Action == "machine.create" || c.Action == "network.create") && !r.Resource.Complete {
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
	net, err := e.invoke(ctx, Command{Action: "network.create", Operation: i.ID + "/network", Owner: i.ID, Environment: env, VLAN: i.VLAN})
	if err != nil {
		return e.failed(*i, err)
	}
	i.Resources = upsert(i.Resources, net.Resource)
	if err = e.state.Put(ctx, "instance", i.ID, i); err != nil {
		return err
	}
	// The network is the only machine dependency. Persist each completed sibling
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
			r, err := e.invoke(ctx, Command{Action: "machine.create", Operation: i.ID + "/machine/" + m.Name, Owner: i.ID, Environment: env, Machine: m, Network: net.Resource, VLAN: i.VLAN})
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
	unlock, err := e.state.LockKey("instance:" + instance)
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
		r, err := e.provider.Execute(ctx, cmd, e.observer)
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
	for n := len(i.Resources) - 1; n >= 0; n-- {
		if r := i.Resources[n]; r.Kind != "machine" {
			if err := destroy(r); err != nil {
				return err
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
	return e.withAllocationLock(ctx, func() error {
		var im ImageRecord
		if err := e.state.Get(ctx, "image", key, &im); err != nil {
			return err
		}
		instances, err := e.List(ctx)
		if err != nil {
			return err
		}
		for _, i := range instances {
			if i.Status == "destroyed" {
				continue
			}
			for _, m := range i.Plan.Machines {
				if m.NativeImage == im.Resource.ID && sameTarget(i.Plan.Environment, im.Environment) {
					return problem(Invalid, key, "image is referenced by a retained instance")
				}
			}
		}
		r, err := e.invoke(ctx, Command{Action: "resource.destroy", Operation: im.ID + "/remove", Owner: im.Resource.Owner, Environment: im.Environment, Prior: im.Resource})
		if err != nil {
			return err
		}
		if !r.Absent {
			return problem(RecoveryRequired, key, "image deletion unconfirmed")
		}
		if err = e.state.Release(ctx, im.ID); err != nil {
			return err
		}
		if err = e.state.Delete(ctx, "build", key); err != nil {
			return err
		}
		return e.state.Delete(ctx, "image", key)
	})
}

// WithSSHMaterial holds the machine lock and shared instance lifecycle guard for a
// bounded caller-owned SSH operation. Credentials must stay local.
func (e *Engine) WithSSHMaterial(ctx context.Context, instance, machine string, fn func(Instance, PlannedMachine, Resource, []byte) error) error {
	return e.withMachineLock(ctx, instance, machine, func() error {
		i, m, r, key, err := e.SSHMaterial(ctx, instance, machine)
		if err != nil {
			return err
		}
		if !Contains(m.Spec.Access, "ssh") {
			return problem(Unsupported, machine, "SSH access is not declared")
		}
		return fn(i, m, r, key)
	})
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
	return e.withAllocationLock(ctx, func() error {
		var im ImageRecord
		if err := e.state.Get(ctx, "image", key, &im); err == nil {
			return problem(Invalid, key, "published image: use image remove")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var c Command
		if err := e.state.Get(ctx, "build", key, &c); err != nil {
			return err
		}
		c.Action = "image.abandon"
		c.Operation = c.Owner + "/abandon"
		r, err := e.invoke(ctx, c)
		if err != nil {
			return err
		}
		if !r.Absent {
			return problem(RecoveryRequired, key, "build cleanup unconfirmed")
		}
		if err = e.state.Release(ctx, c.Owner); err != nil {
			return err
		}
		return e.state.Delete(ctx, "build", key)
	})
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
