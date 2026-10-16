package hosted

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	lc "github.com/OutboundSpade/LabCentral/v2"
)

// SiteCapacity is an admission ceiling, independent of weekly student time.
// Both zero disables it. Reservations include queued/failed/ending allocations
// until native absence has been confirmed and the hosted record is ended.
type SiteCapacity struct {
	VCPU      int `json:"vCPU,omitempty"`
	MemoryMiB int `json:"memoryMiB,omitempty"`
}

func (c SiteCapacity) Validate() error {
	if c.VCPU == 0 && c.MemoryMiB == 0 {
		return nil
	}
	if c.VCPU < 1 || c.VCPU > 1000000 || c.MemoryMiB < 1 || c.MemoryMiB > 1000000000 {
		return fmt.Errorf("capacity requires both vCPU (1–1000000) and memoryMiB (1–1000000000), or both zero")
	}
	return nil
}
func planCapacity(p lc.Plan) (SiteCapacity, error) {
	c := SiteCapacity{}
	for _, m := range p.Machines {
		c.VCPU += m.CPU
		c.MemoryMiB += m.MemoryMiB
	}
	switch p.Environment.Platform {
	case "proxmox":
		c.VCPU++
		c.MemoryMiB += 768
	case "aws":
		gateway := p.Environment.AWS.Gateway.Effective()
		found := false
		for _, t := range p.Environment.AWS.Types {
			if t.Name == gateway.InstanceType {
				c.VCPU += t.CPU
				c.MemoryMiB += t.MemoryMiB
				found = true
				break
			}
		}
		if !found {
			return c, fmt.Errorf("Configure the gateway instance type in environment.aws.types for capacity accounting.")
		}
	default:
		return c, fmt.Errorf("capacity cannot resolve allocation platform")
	}
	if c.VCPU < 1 || c.MemoryMiB < 1 {
		return c, fmt.Errorf("capacity cannot resolve allocation size")
	}
	return c, nil
}

// Call inside Store.atomic. Hosted decisions serialize on the database writer,
// so the new meeting/run and its capacity reservation commit together. Sources
// stay pinned; no provider or identity network calls are made under this lock.
func (s *Service) checkCapacity(ctx context.Context, additional SiteCapacity, replacingBuild ...string) error {
	limit := s.Config.Capacity
	if limit == (SiteCapacity{}) {
		return nil
	}
	if err := limit.Validate(); err != nil {
		return err
	}
	allocations := map[string]SiteCapacity{}
	add := func(instance, revision string) error {
		var source lc.Source
		if err := s.Store.Get(ctx, "source", revision, &source); err != nil {
			return fmt.Errorf("Capacity is unavailable: a reserved source cannot be read.")
		}
		plan, err := s.planSource(source)
		if err != nil {
			return fmt.Errorf("Capacity is unavailable: a reserved plan cannot be resolved.")
		}
		size, err := planCapacity(plan)
		if err != nil {
			return err
		}
		allocations[instance] = size
		return nil
	}
	meeting, err := s.Store.Meeting(ctx)
	if err != nil {
		return err
	}
	if meeting != nil && meeting.Status != "ended" {
		teams, err := s.Store.Teams(ctx, meeting.ID)
		if err != nil {
			return err
		}
		for _, team := range teams {
			if team.Status != "ended" {
				if err := add(team.InstanceID, meeting.Template); err != nil {
					return err
				}
			}
		}
	}
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT instance_id,lab_id FROM practice_runs WHERE status!='ended'`)
	if err != nil {
		return err
	}
	type reserved struct{ instance, lab string }
	var runs []reserved
	for rows.Next() {
		var run reserved
		if err = rows.Scan(&run.instance, &run.lab); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, run := range runs {
		var publication LearningPublication
		if err = s.Store.Get(ctx, "learning-publication", run.lab, &publication); err != nil {
			return err
		}
		if err = add(run.instance, publication.Template); err != nil {
			return err
		}
	}
	// Engine inventory is local durable state. It covers retained or orphaned
	// allocations as well, without double-counting the hosted reservation.
	instances, err := s.Engine.List(ctx)
	if err != nil {
		return fmt.Errorf("Capacity inventory is unavailable.")
	}
	for _, instance := range instances {
		if instance.Status == "destroyed" {
			continue
		}
		size, err := planCapacity(instance.Plan)
		if err != nil {
			return err
		}
		reserved := allocations[instance.ID]
		allocations[instance.ID] = SiteCapacity{max(size.VCPU, reserved.VCPU), max(size.MemoryMiB, reserved.MemoryMiB)}
	}
	builds, err := s.Store.List(ctx, "capacity-build")
	if err != nil {
		return err
	}
	used := additional
	coveredBuilds := map[string]bool{}
	for _, digest := range replacingBuild {
		if digest != "" {
			coveredBuilds[digest] = true
		}
	}
	for _, raw := range builds {
		var size buildCapacityReservation
		if err = json.Unmarshal(raw, &size); err != nil {
			return err
		}
		used.VCPU += size.VCPU
		used.MemoryMiB += size.MemoryMiB
		if size.SourceDigest != "" {
			coveredBuilds[size.SourceDigest] = true
		}
	}
	// Retained native build intents can predate hosted reservations. Count
	// every uncovered intent conservatively until its cleanup is confirmed.
	nativeBuilds, err := s.Engine.Builds(ctx)
	if err != nil {
		return fmt.Errorf("Build capacity inventory is unavailable.")
	}
	published, err := s.Engine.Images(ctx)
	if err != nil {
		return fmt.Errorf("Image capacity inventory is unavailable.")
	}
	readyKeys := map[string]bool{}
	for _, image := range published {
		readyKeys[image.Key] = true
	}
	for _, build := range nativeBuilds {
		// Publication confirms disposable builder/qualifier cleanup. Retained
		// build provenance for a stopped image consumes no running capacity.
		if readyKeys[build.Image.CacheKey] {
			continue
		}
		if coveredBuilds[build.Source.Digest] {
			continue
		}
		size, err := temporaryBuildCapacity(build.Environment, 1)
		if err != nil {
			return err
		}
		used.VCPU += size.VCPU
		used.MemoryMiB += size.MemoryMiB
	}
	for _, size := range allocations {
		used.VCPU += size.VCPU
		used.MemoryMiB += size.MemoryMiB
	}
	if used.VCPU > limit.VCPU || used.MemoryMiB > limit.MemoryMiB {
		return fmt.Errorf("Site capacity exceeded: reservations need %d vCPU / %d MiB; the configured ceiling is %d vCPU / %d MiB. End a lab and wait for confirmed cleanup before retrying.", used.VCPU, used.MemoryMiB, limit.VCPU, limit.MemoryMiB)
	}
	return nil
}
func (s *Service) revisionCapacity(ctx context.Context, revision string, count int) (SiteCapacity, error) {
	if s.Config.Capacity == (SiteCapacity{}) {
		return SiteCapacity{}, nil
	}
	plan, err := s.plan(ctx, revision)
	if err != nil {
		return SiteCapacity{}, err
	}
	size, err := planCapacity(plan)
	return SiteCapacity{size.VCPU * count, size.MemoryMiB * count}, err
}

// Build reservations cover each parallel builder or its boot qualifier plus
// gateway. They survive a failed/unknown build until retry or image cleanup
// confirms temporary resources are absent.
func (s *Service) reserveBuildCapacity(ctx context.Context, revision string, plan lc.Plan) error {
	if s.Config.Capacity == (SiteCapacity{}) {
		return nil
	}
	return s.Store.atomic(ctx, func(ctx context.Context) error {
		count := 0
		for _, image := range plan.Images {
			if image.BuildProgram() != "" {
				count++
			}
		}
		count = min(count, s.Config.Engine.Effective().MaxImageBuildConcurrency)
		size, err := temporaryBuildCapacity(plan.Environment, count)
		if err != nil {
			return err
		}
		var previous buildCapacityReservation
		err = s.Store.Get(ctx, "capacity-build", revision, &previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		size = SiteCapacity{max(size.VCPU, previous.VCPU), max(size.MemoryMiB, previous.MemoryMiB)}
		// Temporarily remove our old reservation within this transaction to avoid
		// counting it twice on retry. Rollback restores it on any failed admission.
		if err := s.Store.Delete(ctx, "capacity-build", revision); err != nil {
			return err
		}
		if err := s.checkCapacity(ctx, size, plan.Source.Digest); err != nil {
			return err
		}
		return s.Store.Put(ctx, "capacity-build", revision, buildCapacityReservation{size, plan.Source.Digest})
	})
}

type buildCapacityReservation struct {
	SiteCapacity
	SourceDigest string `json:"sourceDigest,omitempty"`
}

func temporaryBuildCapacity(env lc.Environment, count int) (SiteCapacity, error) {
	if env.Platform == "proxmox" {
		return SiteCapacity{count * 3, count * (4096 + 768)}, nil
	}
	if env.Platform != "aws" {
		return SiteCapacity{}, fmt.Errorf("Build capacity platform is unavailable.")
	}
	// Resolve the gateway through the allowlist too; unknown sizes fail closed.
	if _, err := planCapacity(lc.Plan{Environment: env}); err != nil {
		return SiteCapacity{}, err
	}
	size := SiteCapacity{}
	for _, t := range env.AWS.Types {
		size.VCPU = max(size.VCPU, t.CPU)
		size.MemoryMiB = max(size.MemoryMiB, t.MemoryMiB)
	}
	return SiteCapacity{size.VCPU * 2 * count, size.MemoryMiB * 2 * count}, nil
}
