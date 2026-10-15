package cli

import (
	"encoding/json"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"text/tabwriter"
)

func readable(out io.Writer, value any) bool {
	switch v := value.(type) {
	case lc.Plan:
		fmt.Fprintf(out, "%s on %s (%s)\nIsolated IPv4 networks; Internet egress is configured per network.\n", v.Source.Lab.Name, v.Environment.Name, v.Environment.Platform)
		for _, im := range v.Images {
			if im.BuildProgram() != "" {
				fmt.Fprintf(out, "Image %s: build %s from %s; reuse only after cache verification\n", im.Name, im.BuildProgram(), im.Base.ID)
			} else {
				fmt.Fprintf(out, "Image %s: use native base %s\n", im.Name, im.Base.ID)
			}
		}
		if v.VPCCIDR != "" {
			fmt.Fprintf(out, "VPC %s; edge %s; AZ %s\n", v.VPCCIDR, v.EdgeCIDR, v.Environment.AWS.Zone)
		}
		for _, n := range v.Networks {
			fmt.Fprintf(out, "Network %s: %s, egress %s", n.Name, n.CIDR, n.Egress)
			if n.VLAN > 0 {
				fmt.Fprintf(out, ", VLAN %d", n.VLAN)
			}
			fmt.Fprintln(out)
		}
		for _, m := range v.Machines {
			fmt.Fprintf(out, "Machine %s: %s, %d CPU / %d MiB / %d GiB, access %v\n", m.Name, m.InstanceType, m.CPU, m.MemoryMiB, m.DiskGiB, m.Spec.Access)
			options := any(m.Spec.Platform.AWS)
			if v.Environment.Platform == "proxmox" {
				options = m.Spec.Platform.Proxmox
			}
			if raw, _ := json.Marshal(options); string(raw) != "null" {
				fmt.Fprintf(out, "  Platform options: %s\n", raw)
			} else {
				fmt.Fprintln(out, "  Platform options: defaults")
			}
			for j, nic := range m.NICs {
				fmt.Fprintf(out, "  NIC %d: %s %s", j, nic.Network, nic.Address)
				if j == 0 {
					fmt.Fprint(out, " (management)")
				}
				if nic.Default {
					fmt.Fprintf(out, " default via %s", nic.Gateway)
				}
				fmt.Fprintln(out)
				for _, route := range nic.Routes {
					if route.Router != "" {
						fmt.Fprintf(out, "    Route %s via %s (%s)\n", route.CIDR, route.Via, route.Router)
					} else {
						fmt.Fprintf(out, "    Route %s via %s\n", route.CIDR, route.Via)
					}
				}
			}
		}
		for _, notice := range v.Notices {
			fmt.Fprintln(out, notice)
		}
	case lc.Instance:
		fmt.Fprintf(out, "%s  %s  %s\n", v.ID, v.Name, v.Status)
		for _, r := range v.Resources {
			fmt.Fprintf(out, "  %s %s: %s %s\n", r.Kind, r.Name, r.ID, r.Address)
		}
		if v.Error != "" {
			fmt.Fprintln(out, v.Error)
		}
	case []lc.Instance:
		if len(v) == 0 {
			fmt.Fprintln(out, "No instances.")
		}
		if len(v) > 0 {
			table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "ID\tNAME\tSTATUS")
			for _, i := range v {
				fmt.Fprintf(table, "%s\t%s\t%s\n", i.ID, i.Name, i.Status)
			}
			_ = table.Flush()
		}
	case []lc.ImageRecord:
		if len(v) == 0 {
			fmt.Fprintln(out, "No images.")
		}
		if len(v) > 0 {
			table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "KEY\tNAME\tNATIVE ID")
			for _, i := range v {
				fmt.Fprintf(table, "%s\t%s\t%s\n", i.Key, i.Name, i.Resource.ID)
			}
			_ = table.Flush()
		}
	case []lc.Command:
		if len(v) == 0 {
			fmt.Fprintln(out, "No build intents.")
		}
		if len(v) > 0 {
			table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "KEY\tIMAGE\tOWNER")
			for _, c := range v {
				fmt.Fprintf(table, "%s\t%s\t%s\n", c.Image.CacheKey, c.Image.Name, c.Owner)
			}
			_ = table.Flush()
		}
	case lc.AccessInfo:
		fmt.Fprintf(out, "Session: %s\nURL: %s\nExpires: %s\n", v.ID, v.URL, v.Expires)
		if v.Username != "" {
			fmt.Fprintf(out, "Guest username: %s\nGuest password: %s\n", v.Username, v.Password)
		}
	case string:
		fmt.Fprintln(out, v)
	default:
		return false
	}
	return true
}
