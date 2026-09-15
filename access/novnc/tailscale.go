package novnc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	accessendpoint "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/accessendpoint"
)

// TailscaleProfile is an operator-pinned encrypted transport, never a fallback.
type TailscaleProfile struct {
	SchemaVersion string `json:"schemaVersion"`
	TargetID      string `json:"targetId"`
	Hostname      string `json:"hostname"`
	Address       string `json:"address"`
	NodeKey       string `json:"nodeKey"`
}

func ParseTailscaleProfile(contents []byte) (*TailscaleProfile, error) {
	var profile TailscaleProfile
	if len(contents) > 4096 || decodeClosedJSON(contents, &profile) != nil || profile.validate() != nil {
		return nil, fmt.Errorf("tailnet console profile is invalid")
	}
	return &profile, nil
}

func (p TailscaleProfile) validate() error {
	ip, err := netip.ParseAddr(p.Address)
	if p.SchemaVersion != "labcentral.tailscale-vnc/v1" || !identifierPattern.MatchString(p.TargetID) ||
		err != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) ||
		p.Hostname == "" || !strings.HasSuffix(p.Hostname, ".ts.net") || strings.ContainsAny(p.Hostname, "/: \r\n\t") ||
		!strings.HasPrefix(p.NodeKey, "nodekey:") || !validSHA256(strings.TrimPrefix(p.NodeKey, "nodekey:")) {
		return fmt.Errorf("tailnet console profile is invalid")
	}
	return nil
}

type tailscalePeer struct {
	Node struct {
		Name      string
		Key       string
		Addresses []string
		Online    bool
	}
}

func (p TailscaleProfile) matches(endpoint accessendpoint.ProxmoxVNC, peer tailscalePeer) bool {
	if endpoint.TargetID != p.TargetID || endpoint.Host != p.Hostname || endpoint.ServerName != p.Hostname ||
		endpoint.Port < 5900 || endpoint.Port > 5999 || strings.TrimSuffix(peer.Node.Name, ".") != p.Hostname ||
		peer.Node.Key != p.NodeKey || !peer.Node.Online {
		return false
	}
	for _, address := range peer.Node.Addresses {
		if address == p.Address+"/32" {
			return true
		}
	}
	return false
}

func (p TailscaleProfile) connect(ctx context.Context, endpoint accessendpoint.ProxmoxVNC) (net.Conn, error) {
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/tailscale/tailscaled.sock")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirect refused") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock/localapi/v0/whois?addr="+p.Address, nil)
	if err != nil {
		return nil, fmt.Errorf("tailnet identity request failed")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tailnet identity service unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	var peer tailscalePeer
	if err != nil || len(data) > 64<<10 || response.StatusCode != http.StatusOK || json.Unmarshal(data, &peer) != nil || !p.matches(endpoint, peer) {
		return nil, fmt.Errorf("tailnet peer binding rejected")
	}
	return dialTailscaleDevice(ctx, net.JoinHostPort(p.Address, fmt.Sprint(endpoint.Port)))
}
