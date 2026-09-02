package proxmox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type apiVersion struct {
	Release string `json:"release"`
	Version string `json:"version"`
	RepoID  string `json:"repoid"`
	Console string `json:"console"`
}

type apiNode struct {
	Node      string  `json:"node"`
	Status    string  `json:"status"`
	MaxCPU    int64   `json:"maxcpu"`
	CPU       float64 `json:"cpu"`
	MaxMemory int64   `json:"maxmem"`
	Memory    int64   `json:"mem"`
	Uptime    int64   `json:"uptime"`
}

type apiStorageFormats struct {
	Default   string   `json:"default"`
	Supported []string `json:"supported"`
}

type apiStorage struct {
	Storage string            `json:"storage"`
	Type    string            `json:"type"`
	Content string            `json:"content"`
	Formats apiStorageFormats `json:"formats"`
	Active  apiBool           `json:"active"`
	Enabled apiBool           `json:"enabled"`
	Shared  apiBool           `json:"shared"`
	Total   int64             `json:"total"`
	Used    int64             `json:"used"`
	Avail   int64             `json:"avail"`
}

type apiBridge struct {
	Iface     string  `json:"iface"`
	Type      string  `json:"type"`
	Active    apiBool `json:"active"`
	Autostart apiBool `json:"autostart"`
	VLANAware apiBool `json:"bridge_vlan_aware"`
	Ports     string  `json:"bridge_ports"`
	CIDR      string  `json:"cidr"`
	CIDR6     string  `json:"cidr6"`
}

type apiVM struct {
	VMID     int64   `json:"vmid"`
	Name     string  `json:"name"`
	Status   string  `json:"status"`
	Template apiBool `json:"template"`
}

func (client *Client) Discover(ctx context.Context) (Inventory, error) {
	version, err := getAPI[apiVersion](ctx, client, "/version", nil)
	if err != nil {
		return Inventory{}, err
	}
	nodes, err := getAPI[[]apiNode](ctx, client, "/nodes", nil)
	if err != nil {
		return Inventory{}, err
	}
	if len(nodes) > maximumNodes {
		return Inventory{}, fmt.Errorf("Proxmox target returned %d nodes; limit is %d", len(nodes), maximumNodes)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node < nodes[j].Node })
	inventory := Inventory{Version: Version{Release: version.Release, Version: version.Version, RepoID: version.RepoID, Console: version.Console}}
	for _, source := range nodes {
		node := Node{
			Name: source.Node, Status: source.Status, MaxCPU: source.MaxCPU, CPUUsage: source.CPU,
			MaxMemoryBytes: source.MaxMemory, UsedMemoryBytes: source.Memory, UptimeSeconds: source.Uptime,
		}
		inventory.Nodes = append(inventory.Nodes, node)
	}
	// Validate provider-returned node identifiers before using them in request
	// paths. The same normalization runs again after nested inventory is added.
	if err := inventory.normalize(); err != nil {
		return Inventory{}, err
	}
	for index := range inventory.Nodes {
		node := &inventory.Nodes[index]
		if node.Status != "online" {
			continue
		}
		client.discoverNode(ctx, node, &inventory)
		if err := inventory.normalize(); err != nil {
			return Inventory{}, err
		}
	}
	if err := inventory.normalize(); err != nil {
		return Inventory{}, err
	}
	return inventory, nil
}

func (client *Client) discoverNode(ctx context.Context, node *Node, inventory *Inventory) {
	nodePath := url.PathEscape(node.Name)
	storages, err := getAPI[[]apiStorage](ctx, client, "/nodes/"+nodePath+"/storage", url.Values{
		"content": {"images"}, "enabled": {"1"}, "format": {"1"},
	})
	if err != nil {
		inventory.Issues = append(inventory.Issues, discoveryIssue(node.Name, "storage", err))
	} else if len(storages) > maximumInventoryItems {
		inventory.Issues = append(inventory.Issues, DiscoveryIssue{Node: node.Name, Scope: "storage", Code: "item_limit_exceeded"})
	} else {
		for _, source := range storages {
			inventory.Storages = append(inventory.Storages, Storage{
				Node: node.Name, ID: source.Storage, Type: source.Type, Content: splitList(source.Content),
				Formats: source.Formats.Supported, DefaultFormat: source.Formats.Default,
				Active: bool(source.Active), Enabled: bool(source.Enabled), Shared: bool(source.Shared),
				TotalBytes: source.Total, UsedBytes: source.Used, AvailableBytes: source.Avail,
			})
		}
	}
	bridges, err := getAPI[[]apiBridge](ctx, client, "/nodes/"+nodePath+"/network", url.Values{"type": {"bridge"}})
	if err != nil {
		inventory.Issues = append(inventory.Issues, discoveryIssue(node.Name, "network", err))
	} else if len(bridges) > maximumInventoryItems {
		inventory.Issues = append(inventory.Issues, DiscoveryIssue{Node: node.Name, Scope: "network", Code: "item_limit_exceeded"})
	} else {
		for _, source := range bridges {
			if source.Type != "bridge" && source.Type != "" {
				continue
			}
			inventory.Bridges = append(inventory.Bridges, Bridge{
				Node: node.Name, Name: source.Iface, Active: bool(source.Active), Autostart: bool(source.Autostart),
				VLANAware: bool(source.VLANAware), Ports: splitList(source.Ports), CIDR: source.CIDR, CIDR6: source.CIDR6,
			})
		}
	}
	vms, err := getAPI[[]apiVM](ctx, client, "/nodes/"+nodePath+"/qemu", url.Values{"full": {"0"}})
	if err != nil {
		inventory.Issues = append(inventory.Issues, discoveryIssue(node.Name, "qemu", err))
	} else if len(vms) > maximumInventoryItems {
		inventory.Issues = append(inventory.Issues, DiscoveryIssue{Node: node.Name, Scope: "qemu", Code: "item_limit_exceeded"})
	} else {
		node.QEMUAPI = true
		for _, source := range vms {
			if bool(source.Template) {
				inventory.Templates = append(inventory.Templates, Template{Node: node.Name, VMID: source.VMID, Name: source.Name, Status: source.Status})
			}
		}
	}
}

func discoveryIssue(node, scope string, err error) DiscoveryIssue {
	code := "invalid_response"
	var statusError *APIStatusError
	if errors.As(err, &statusError) {
		switch {
		case statusError.StatusCode == http.StatusForbidden:
			code = "permission_denied"
		case statusError.StatusCode == http.StatusUnauthorized:
			code = "authentication_failed"
		case statusError.StatusCode >= 500:
			code = "api_unavailable"
		default:
			code = "api_error"
		}
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		code = "request_interrupted"
	} else if strings.Contains(err.Error(), "failed:") {
		code = "transport_error"
	}
	return DiscoveryIssue{Node: node, Scope: scope, Code: code}
}

func splitList(value string) []string {
	return sortedUnique(strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }))
}
