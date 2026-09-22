package proxmox

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
)

// VNCWebSocket keeps the management credential and ticket entirely inside the
// provider. The returned connection is already TLS-authenticated to the target.
func (client *Client) VNCWebSocket(ctx context.Context, node string, vmid uint32) (net.Conn, string, error) {
	fail := func() (net.Conn, string, error) { return nil, "", fmt.Errorf("Proxmox encrypted console unavailable") }
	if !nodeNamePattern.MatchString(node) || vmid < 100 {
		return fail()
	}
	type response struct {
		Port     apiUint16 `json:"port"`
		Ticket   string    `json:"ticket"`
		Password string    `json:"password"`
	}
	path := fmt.Sprintf("/nodes/%s/qemu/%d", node, vmid)
	proxy, err := requestAPI[response](ctx, client, "POST", path+"/vncproxy", url.Values{"websocket": {"1"}, "generate-password": {"1"}})
	if err != nil || proxy.Port < 5900 || proxy.Port > 5999 || len(proxy.Ticket) == 0 || len(proxy.Ticket) > 512 || len(proxy.Password) == 0 || len(proxy.Password) > 256 {
		return fail()
	}
	endpoint := *client.endpoint
	endpoint.Scheme = "wss"
	endpoint.Path = "/api2/json" + path + "/vncwebsocket"
	endpoint.RawPath = ""
	endpoint.RawQuery = url.Values{"port": {fmt.Sprint(proxy.Port)}, "vncticket": {proxy.Ticket}}.Encode()
	httpClient := *client.httpClient
	httpClient.Timeout = 0 // the upgraded stream has a separate fixed relay lifetime
	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(dialCtx, endpoint.String(), &websocket.DialOptions{
		HTTPClient:   &httpClient,
		HTTPHeader:   http.Header{"Authorization": {"PVEAPIToken=" + client.tokenID + "=" + client.tokenSecret}},
		Subprotocols: []string{"binary"},
	})
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return fail() // websocket errors may contain the upstream ticket URL
	}
	if conn.Subprotocol() != "binary" {
		_ = conn.CloseNow()
		return fail()
	}
	conn.SetReadLimit(4 << 20)
	return websocket.NetConn(context.Background(), conn, websocket.MessageBinary), proxy.Password, nil
}
