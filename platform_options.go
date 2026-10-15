package labcentral

import (
	"reflect"
	"sort"
	"strings"
)

// AWSBurstable reports the instance families which accept CPU credit policy.
func AWSBurstable(instanceType string) bool {
	family, _, found := strings.Cut(instanceType, ".")
	return found && Contains([]string{"t2", "t3", "t3a", "t4g"}, family)
}

func selectAWSInstanceType(m Machine, a AWSConfig, field string) (InstanceType, error) {
	memory, _ := SizeMiB(m.Memory)
	requested := ""
	if m.Platform.AWS != nil {
		requested = m.Platform.AWS.InstanceType
	}
	types := append([]InstanceType(nil), a.Types...)
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
		if requested != "" && t.Name != requested {
			continue
		}
		if t.CPU >= m.CPU && t.MemoryMiB >= memory {
			return t, nil
		}
		if requested != "" {
			return InstanceType{}, problem(Invalid, field+".platform.aws.instanceType", "instance type does not satisfy cpu/memory")
		}
	}
	if requested != "" {
		return InstanceType{}, problem(Unsupported, field+".platform.aws.instanceType", "instance type is not permitted by environment.aws.types")
	}
	return InstanceType{}, problem(Unsupported, field, "no allowed instance type satisfies requirements")
}

// CheckPlatformOptions validates the selected platform's requests against the
// operator environment. Other platforms' blocks have no effect on this check.
// Adapters repeat this check before native work, including their check action.
func CheckPlatformOptions(l Lab, e Environment) error {
	for _, n := range sortedKeys(l.Images) {
		i := l.Images[n]
		if e.Platform == "aws" && i.Platform.AWS != nil && i.Platform.AWS.BuildInstanceType != "" {
			if !awsTypeAllowed(e.AWS, i.Platform.AWS.BuildInstanceType) {
				return problem(Unsupported, "images."+n+".platform.aws.buildInstanceType", "instance type is not permitted by environment.aws.types")
			}
		}
	}
	for _, n := range sortedKeys(l.Machines) {
		m, f := l.Machines[n], "machines."+n
		if e.Platform == "aws" && m.Platform.AWS != nil {
			o := m.Platform.AWS
			if err := validateAWSMachineOptions(o, f+".platform.aws"); err != nil {
				return err
			}
			if o.InstanceType != "" || o.CPUCredits != "" {
				selected, err := selectAWSInstanceType(m, e.AWS, f)
				if err != nil {
					return err
				}
				if o.CPUCredits != "" && !AWSBurstable(selected.Name) {
					return problem(Invalid, f+".platform.aws.cpuCredits", "CPU credits require a t2/t3/t3a/t4g instance type")
				}
			}
			if o.Volume != nil {
				disk, err := SizeMiB(m.Disk)
				if err != nil {
					return problem(Invalid, f+".disk", err.Error())
				}
				iops, throughput := 3000, 125
				if o.Volume.IOPS != nil {
					iops = *o.Volume.IOPS
				}
				if o.Volume.Throughput != nil {
					throughput = *o.Volume.Throughput
				}
				if iops > 500*(disk/1024) {
					return problem(Invalid, f+".platform.aws.volume.iops", "gp3 IOPS must be at most 500 per GiB")
				}
				if throughput*4 > iops {
					return problem(Invalid, f+".platform.aws.volume.throughput", "gp3 throughput must be at most IOPS/4 MiB/s")
				}
			}
		}
		if e.Platform == "proxmox" && m.Platform.Proxmox != nil {
			o := m.Platform.Proxmox
			if err := validateProxmoxMachineOptions(o, f+".platform.proxmox"); err != nil {
				return err
			}
			if o.CPUType != "" && !Contains(e.Proxmox.CPUTypes, o.CPUType) {
				return problem(Unsupported, f+".platform.proxmox.cpuType", "CPU model is not permitted by environment.proxmox.cpuTypes; omit it to preserve the base default")
			}
		}
	}
	return nil
}
func awsTypeAllowed(a AWSConfig, name string) bool {
	for _, t := range a.Types {
		if t.Name == name {
			return true
		}
	}
	return false
}
func validateAWSMachineOptions(o *AWSMachineOptions, f string) error {
	if o.CPUCredits != "" && !Contains([]string{"standard", "unlimited"}, o.CPUCredits) {
		return problem(Invalid, f+".cpuCredits", "expected standard or unlimited")
	}
	if v := o.Volume; v != nil {
		if v.Type != "" && v.Type != "gp3" {
			return problem(Invalid, f+".volume.type", "only gp3 is implemented")
		}
		if v.IOPS != nil && (*v.IOPS < 3000 || *v.IOPS > 16000) {
			return problem(Invalid, f+".volume.iops", "expected 3000–16000")
		}
		if v.Throughput != nil && (*v.Throughput < 125 || *v.Throughput > 1000) {
			return problem(Invalid, f+".volume.throughput", "expected 125–1000 MiB/s")
		}
	}
	return nil
}
func validateProxmoxMachineOptions(o *ProxmoxMachineOptions, f string) error {
	if o.NICModel != "" && !Contains([]string{"virtio", "e1000", "e1000e", "vmxnet3", "rtl8139"}, o.NICModel) {
		return problem(Invalid, f+".nicModel", "unsupported NIC model")
	}
	return nil
}

// Check all present fields, including explicit false/zero pointer values. This
// also fences future typed options until the adapter declares their support.
func checkImplementedOptions(options any, accepted []string, field string) error {
	var walk func(reflect.Value, string) error
	walk = func(v reflect.Value, prefix string) error {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			v = v.Elem()
		}
		for i := 0; i < v.NumField(); i++ {
			value := v.Field(i)
			if value.IsZero() {
				continue
			}
			name := strings.Split(v.Type().Field(i).Tag.Get("yaml"), ",")[0]
			path := prefix + name
			if !Contains(accepted, path) {
				return problem(Unsupported, field+"."+path, "platform option is not implemented")
			}
			if value.Kind() == reflect.Pointer && value.Type().Elem().Kind() == reflect.Struct {
				if err := walk(value, path+"."); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(options), "")
}

// CheckCommandPlatformOptions also checks standalone planned requests used by
// native image validation and rejects a plan that contradicts an explicit type.
func CheckCommandPlatformOptions(c Command) error {
	if err := CheckWindowsCommand(c); err != nil {
		return err
	}
	if c.Environment.Platform == "aws" {
		if err := CheckAWSNICLimits(c.Source.Lab, c.Environment, nil); err != nil {
			return err
		}
		if c.Machine.Name != "" && c.Machine.Name != "gateway" {
			for _, instance := range c.Environment.AWS.Types {
				if instance.Name == c.Machine.InstanceType && instance.MaxNICs > 0 && len(c.Machine.Spec.NICs) > instance.MaxNICs {
					return problem(Unsupported, fmtPathIndex("machines."+c.Machine.Name+".nics", instance.MaxNICs), "NIC count exceeds instance type ENI limit")
				}
			}
		}
	}

	if err := CheckPlatformOptions(c.Source.Lab, c.Environment); err != nil {
		return err
	}
	if c.Machine.Name != "" {
		if err := CheckPlatformOptions(Lab{Machines: map[string]Machine{c.Machine.Name: c.Machine.Spec}}, c.Environment); err != nil {
			return err
		}
		if c.Environment.Platform == "aws" && c.Machine.Spec.Platform.AWS != nil {
			o := c.Machine.Spec.Platform.AWS
			f := "machines." + c.Machine.Name + ".platform.aws."
			if o.InstanceType != "" && c.Machine.InstanceType != "" && c.Machine.InstanceType != o.InstanceType {
				return problem(Invalid, f+"instanceType", "planned instance type differs from request")
			}
			if o.CPUCredits != "" && c.Machine.InstanceType != "" && !AWSBurstable(c.Machine.InstanceType) {
				return problem(Invalid, f+"cpuCredits", "planned instance type is not burstable")
			}
		}
	}
	if c.Environment.Platform == "aws" && c.Image.BuildInstanceType != "" && c.Image.BuildInstanceType != c.Environment.Build.InstanceType {
		if !awsTypeAllowed(c.Environment.AWS, c.Image.BuildInstanceType) {
			return problem(Unsupported, "images."+c.Image.Name+".platform.aws.buildInstanceType", "planned build instance type is not permitted by environment.aws.types")
		}
	}
	return nil
}
