//go:build faultinject

package nativeproxy

import (
	"context"
	"encoding/json"
	"github.com/OutboundSpade/LabCentral/v2/internal/faultinject"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestPackerProxyPreservesEmptyPostAndResponseLoss(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || len(r.TransferEncoding) != 0 || r.ContentLength != 0 {
			t.Errorf("empty POST changed: %s %v %d", r.Method, r.TransferEncoding, r.ContentLength)
		}
		w.Write([]byte(`{"data":"accepted"}`))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	endpoint, close, err := Start(faultinject.WithOperation(context.Background(), "proxmox", "image.build"), Config{Upstream: target, Protocol: "proxmox", Step: func(*http.Request, []byte) string { return "packer.POST/qemu/{id}/status/start" }})
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	response, err := http.Post(endpoint+"/api2/json/nodes/node/qemu/2000/status/start", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	b, _ := json.Marshal(map[string]any{"operation": "proxmox/image.build", "step": "packer.POST/qemu/{id}/status/start", "fault": "response-loss", "marker": filepath.Join(t.TempDir(), "receipt")})
	t.Setenv("LABCENTRAL_FAULT", string(b))
	response, err = http.Post(endpoint+"/api2/json/nodes/node/qemu/2000/status/start", "application/x-www-form-urlencoded", nil)
	if err == nil {
		response.Body.Close()
		t.Fatal("real native response was not dropped")
	}
	if calls.Load() != 2 {
		t.Fatal("native mutation not performed", calls.Load())
	}
}
