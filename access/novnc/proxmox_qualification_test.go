package novnc

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const proxmoxAccessQualificationSentinel = "I_ACKNOWLEDGE_LABCENTRAL_PROXMOX_ACCESS_TEST"

func TestProxmoxAccessSessionQualification(t *testing.T) {
	environment := map[string]string{
		"execute":          os.Getenv("LABCENTRAL_PROXMOX_ACCESS_EXECUTE"),
		"baseURL":          os.Getenv("LABCENTRAL_PROXMOX_ACCESS_BASE_URL"),
		"ownerTokenFile":   os.Getenv("LABCENTRAL_PROXMOX_ACCESS_OWNER_TOKEN_FILE"),
		"deniedTokenFile":  os.Getenv("LABCENTRAL_PROXMOX_ACCESS_DENIED_TOKEN_FILE"),
		"instanceID":       os.Getenv("LABCENTRAL_PROXMOX_ACCESS_INSTANCE_ID"),
		"resourceID":       os.Getenv("LABCENTRAL_PROXMOX_ACCESS_RESOURCE_ID"),
		"managementOrigin": os.Getenv("LABCENTRAL_PROXMOX_ACCESS_MANAGEMENT_ORIGIN"),
		"publicCAFile":     os.Getenv("LABCENTRAL_PROXMOX_ACCESS_PUBLIC_CA_FILE"),
	}
	if environment["execute"] == "" {
		for name, value := range environment {
			if name != "execute" && value != "" {
				t.Fatalf("partial protected access activation: %s is set without LABCENTRAL_PROXMOX_ACCESS_EXECUTE", name)
			}
		}
		t.Skip("protected Proxmox access qualification is not activated")
	}
	if environment["execute"] != proxmoxAccessQualificationSentinel {
		t.Fatal("protected Proxmox access qualification sentinel is invalid")
	}
	for _, name := range []string{"baseURL", "ownerTokenFile", "deniedTokenFile", "instanceID", "resourceID", "managementOrigin"} {
		if environment[name] == "" {
			t.Fatalf("protected Proxmox access qualification requires %s", name)
		}
	}
	base, err := url.Parse(environment["baseURL"])
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		t.Fatal("LABCENTRAL_PROXMOX_ACCESS_BASE_URL must be an exact HTTPS origin")
	}
	base.Path = ""
	ownerToken := readProtectedQualificationSecret(t, environment["ownerTokenFile"])
	deniedToken := readProtectedQualificationSecret(t, environment["deniedTokenFile"])
	transport := &http.Transport{
		Proxy:             nil,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	if environment["publicCAFile"] != "" {
		caPEM, err := os.ReadFile(environment["publicCAFile"])
		if err != nil {
			t.Fatalf("read public CA file: %v", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			t.Fatalf("load system trust: %v", err)
		}
		if !roots.AppendCertsFromPEM(caPEM) {
			t.Fatal("public CA file contains no certificates")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	client := &http.Client{
		Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirect refused") },
	}
	t.Cleanup(transport.CloseIdleConnections)

	body := map[string]any{
		"instanceId": environment["instanceID"], "resourceId": environment["resourceID"],
		"method": "vnc", "durationSeconds": 300,
	}
	denied := qualificationJSONRequest(t, client, http.MethodPost, base.String()+"/api/v1/access-sessions", deniedToken, "access-qualified-denied", "", body)
	if denied.status != http.StatusForbidden && denied.status != http.StatusNotFound {
		t.Fatalf("cross-tenant access returned HTTP %d, want 403 or 404", denied.status)
	}
	created := qualificationJSONRequest(t, client, http.MethodPost, base.String()+"/api/v1/access-sessions", ownerToken, "access-qualified-owner", "", body)
	if created.status != http.StatusCreated || created.cacheControl != "no-store" || created.referrerPolicy != "no-referrer" {
		t.Fatalf("access creation response = HTTP %d cache=%q referrer=%q", created.status, created.cacheControl, created.referrerPolicy)
	}
	if bytes.Contains(created.body, []byte(environment["managementOrigin"])) {
		t.Fatal("access API exposed the provider management origin")
	}
	var launch struct {
		Session struct {
			ID              string `json:"id"`
			State           string `json:"state"`
			ResourceVersion uint64 `json:"resourceVersion"`
		} `json:"session"`
		LaunchURL    string `json:"launchUrl"`
		OneTimeToken string `json:"oneTimeToken"`
	}
	if err := json.Unmarshal(created.body, &launch); err != nil {
		t.Fatalf("decode access creation response: %v", err)
	}
	launchURL, err := url.Parse(launch.LaunchURL)
	if err != nil || launch.Session.ID == "" || launch.Session.State != "ready" || launch.Session.ResourceVersion == 0 ||
		len(launch.OneTimeToken) < 43 || launchURL.RawQuery != "" || launchURL.Fragment != "" || strings.Contains(launch.LaunchURL, launch.OneTimeToken) {
		t.Fatal("access creation returned an invalid or URL-exposed launch token")
	}

	websocketURL := *base
	websocketURL.Scheme = "wss"
	websocketURL.Path = "/access-proxy/v1/access/" + url.PathEscape(launch.Session.ID) + "/websocket"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(ctx, websocketURL.String(), &websocket.DialOptions{
		HTTPClient: client, HTTPHeader: http.Header{"Origin": []string{base.String()}}, Subprotocols: []string{"binary"},
	})
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("open qualified console WebSocket: HTTP %d: %v", status, err)
	}
	defer connection.CloseNow()
	launchMessage, _ := json.Marshal(map[string]string{"accessSessionId": launch.Session.ID, "oneTimeToken": launch.OneTimeToken})
	if err := connection.Write(ctx, websocket.MessageText, launchMessage); err != nil {
		t.Fatalf("send one-time access token: %v", err)
	}
	messageType, acknowledgement, err := connection.Read(ctx)
	if err != nil || messageType != websocket.MessageText || string(acknowledgement) != `{"authorized":true}` {
		t.Fatalf("access authorization acknowledgement was not received: %v", err)
	}
	stream := websocket.NetConn(context.Background(), connection, websocket.MessageBinary)
	version := make([]byte, len(rfb38))
	if _, err := io.ReadFull(stream, version); err != nil || !bytes.Equal(version, rfb38) {
		t.Fatalf("qualified console did not present RFB: %v", err)
	}

	revoked := qualificationJSONRequest(t, client, http.MethodPost,
		base.String()+"/api/v1/access-sessions/"+url.PathEscape(launch.Session.ID)+"/actions/revoke",
		ownerToken, "access-qualified-revoke", fmt.Sprintf(`"%d"`, launch.Session.ResourceVersion), map[string]string{"reason": "qualification"})
	if revoked.status != http.StatusOK {
		t.Fatalf("access revocation returned HTTP %d", revoked.status)
	}
	var terminal struct {
		State           string `json:"state"`
		ClosureRequired bool   `json:"closureRequired"`
	}
	if err := json.Unmarshal(revoked.body, &terminal); err != nil || terminal.State != "revoked" || terminal.ClosureRequired {
		t.Fatalf("access revocation did not reach closed terminal state: state=%q closureRequired=%v err=%v", terminal.State, terminal.ClosureRequired, err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := stream.Read(make([]byte, 1)); err == nil {
		t.Fatal("revocation did not close the active console stream")
	}
}

type qualificationResponse struct {
	status         int
	body           []byte
	cacheControl   string
	referrerPolicy string
}

func qualificationJSONRequest(t *testing.T, client *http.Client, method, endpoint, token, idempotencyKey, ifMatch string, body any) qualificationResponse {
	t.Helper()
	contents, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, endpoint, bytes.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("qualified access API request failed: %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(responseBody) > 64<<10 {
		t.Fatal("qualified access API response exceeded its bound or could not be read")
	}
	return qualificationResponse{
		status: response.StatusCode, body: responseBody, cacheControl: response.Header.Get("Cache-Control"),
		referrerPolicy: response.Header.Get("Referrer-Policy"),
	}
}

func readProtectedQualificationSecret(t *testing.T, path string) string {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Fatal("protected qualification token path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("protected qualification token must be a private regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read protected qualification token")
	}
	value := strings.TrimSpace(string(contents))
	clear(contents)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\t ") {
		t.Fatal("protected qualification token is invalid")
	}
	return value
}
