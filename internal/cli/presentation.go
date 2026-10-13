package cli

import (
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"io"
	"text/tabwriter"
)

func readable(out io.Writer, value any) bool {
	switch v := value.(type) {
	case lc.Plan:
		fmt.Fprintf(out, "%s on %s (%s)\nPrivate IPv4 network; general Internet egress denied.\n", v.Source.Lab.Name, v.Environment.Name, v.Environment.Platform)
		for _, im := range v.Images {
			if im.Playbook != "" {
				fmt.Fprintf(out, "Image %s: build %s from %s; reuse only after cache verification\n", im.Name, im.Playbook, im.Base.ID)
			} else {
				fmt.Fprintf(out, "Image %s: use native base %s\n", im.Name, im.Base.ID)
			}
		}
		for _, m := range v.Machines {
			fmt.Fprintf(out, "Machine %s: %s, %d CPU / %d MiB / %d GiB, access %v\n", m.Name, m.InstanceType, m.CPU, m.MemoryMiB, m.DiskGiB, m.Spec.Access)
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
