package labcentral

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
)

var extensionPattern = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9-]+)+$`)

func fmtPathIndex(f string, i int) string { return fmt.Sprintf("%s[%d]", f, i) }
func defaultString(b JSONValue) string {
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	return string(b)
}
func validateV3(l Lab) error {
	total := 0
	ext := func(f string, x Extensions) error {
		for _, k := range sortedKeys(x) {
			if !extensionPattern.MatchString(k) {
				return problem(Invalid, f+"."+k, "expected a namespaced extension key")
			}
			if !json.Valid(x[k]) {
				return problem(Invalid, f+"."+k, "expected JSON")
			}
		}
		if x != nil {
			b, _ := json.Marshal(x)
			total += len(b)
		}
		if total > 64<<10 {
			return problem(Invalid, "extensions", "total extensions exceed 64 KiB")
		}
		return nil
	}
	if err := ext("extensions", l.Extensions); err != nil {
		return err
	}
	seenPlatforms := map[string]bool{}
	platforms := []string{}
	if l.Platforms != nil {
		platforms = l.Platforms.Names
	}
	for _, p := range platforms {
		if !Contains([]string{"aws", "proxmox"}, p) || seenPlatforms[p] {
			return problem(Invalid, "platforms", "use unique aws or proxmox entries")
		}
		seenPlatforms[p] = true
	}
	networks := l.Networks
	if networks == nil {
		networks = map[string]Network{"lab": {}}
	} else if len(networks) == 0 {
		return problem(Invalid, "networks", "expected at least one network")
	}
	for _, n := range sortedKeys(networks) {
		v := networks[n]
		f := "networks." + n
		if !namePattern.MatchString(n) {
			return problem(Invalid, f, "invalid network name")
		}
		if v.Egress != "" && !Contains([]string{"none", "internet"}, v.Egress) {
			return problem(Invalid, f+".egress", "expected none or internet")
		}
		if v.CIDR != "" {
			p, e := netip.ParsePrefix(v.CIDR)
			if e != nil || !p.Addr().Is4() || !p.Addr().IsPrivate() || p.Bits() < 16 || p.Bits() > 28 || p != p.Masked() {
				return problem(Invalid, f+".cidr", "expected a canonical private IPv4 /16–/28")
			}
		}
		if err := ext(f+".extensions", v.Extensions); err != nil {
			return err
		}
	}
	for _, n := range sortedKeys(l.Images) {
		v := l.Images[n]
		f := "images." + n
		if v.OS != "" && !Contains([]string{"linux", "windows"}, v.OS) {
			return problem(Invalid, f+".os", "expected linux or windows")
		}
		if v.Build != nil && v.Build.Playbook == "" {
			return problem(Invalid, f+".build.playbook", "playbook is required")
		}
		if v.Build != nil && v.Build.Files != nil && len(v.Build.Files) == 0 {
			return problem(Invalid, f+".build.files", "explicit selection must include the playbook")
		}
		if err := ext(f+".extensions", v.Extensions); err != nil {
			return err
		}
	}
	used := map[string]map[int]bool{}
	for _, n := range sortedKeys(l.Machines) {
		v := l.Machines[n]
		f := "machines." + n
		if v.NICs == nil && len(networks) != 1 {
			return problem(Invalid, f+".nics", "NICs are required when more than one network is declared")
		}
		if v.NICs != nil && len(v.NICs) == 0 {
			return problem(Invalid, f+".nics", "expected at least one NIC")
		}
		for i, nic := range v.NICs {
			nf := fmtPathIndex(f+".nics", i)
			net, ok := networks[nic.Network]
			if !ok {
				return problem(Invalid, nf+".network", "unknown network")
			}
			if nic.Address != nil {
				offset := *nic.Address
				max := 250
				if net.CIDR != "" {
					p, _ := netip.ParsePrefix(net.CIDR)
					usable := (1 << (32 - p.Bits())) - 2
					if usable < max {
						max = usable
					}
				}
				if offset < 4 || offset > max {
					return problem(Invalid, nf+".address", "offset must be 4–250 and within the usable subnet")
				}
				if used[nic.Network] == nil {
					used[nic.Network] = map[int]bool{}
				}
				if used[nic.Network][offset] {
					return problem(Invalid, nf+".address", "duplicate address in network")
				}
				used[nic.Network][offset] = true
			}
		}
		if err := ext(f+".extensions", v.Extensions); err != nil {
			return err
		}
	}
	for _, n := range sortedKeys(l.Inputs) {
		v := l.Inputs[n]
		f := "inputs." + n
		if !namePattern.MatchString(n) {
			return problem(Invalid, f, "invalid name")
		}
		if !Contains([]string{"string", "integer", "boolean"}, v.Type) {
			return problem(Invalid, f+".type", "invalid input type")
		}
		if len(v.Description) > 500 {
			return problem(Invalid, f+".description", "description exceeds 500 bytes")
		}
		if len(v.Default) > 0 {
			if v.Required {
				return problem(Invalid, f+".default", "required inputs cannot have defaults")
			}
			valid := false
			switch v.Type {
			case "string":
				var x string
				valid = json.Unmarshal(v.Default, &x) == nil && string(v.Default) != "null" && len(x) <= 4096
			case "integer":
				_, e := strconv.ParseInt(string(v.Default), 10, 64)
				valid = e == nil
			case "boolean":
				valid = string(v.Default) == "true" || string(v.Default) == "false"
			}
			if !valid {
				return problem(Invalid, f+".default", "default must be a scalar matching the declared type")
			}
		}
	}
	return nil
}
func imageSourceDigest(s Source, n string) string {
	im := s.Lab.Images[n]
	if im.BuildFiles() == nil {
		return s.Digest
	}
	files := []File{}
	for _, f := range s.Files {
		if Contains(im.BuildFiles(), f.Path) {
			files = append(files, f)
		}
	}
	return digest(struct {
		Image      Image
		Files      []File
		Extensions Extensions
	}{im, files, s.Lab.Extensions})
}
