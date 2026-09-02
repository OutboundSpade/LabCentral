package docker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestEngineNegotiatesVersionPullsPinnedImageAndPublishesLoopback(t *testing.T) {
	serviceListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer serviceListener.Close()
	servicePort := serviceListener.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			connection, acceptErr := serviceListener.Accept()
			if acceptErr != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	var mu sync.Mutex
	imagePresent := false
	containerPresent := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case request.URL.Path == "/version":
			writeJSON(response, http.StatusOK, map[string]any{"ApiVersion": "1.55", "Version": "29.7.2", "Os": "linux", "Arch": "amd64"})
		case request.URL.Path == "/v1.55/info":
			writeJSON(response, http.StatusOK, map[string]any{"OperatingSystem": "test", "OSType": "linux"})
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1.55/images/"):
			if !imagePresent {
				writeJSON(response, http.StatusNotFound, map[string]string{"message": "not found"})
				return
			}
			writeJSON(response, http.StatusOK, map[string]any{"Id": "sha256:image"})
		case request.Method == http.MethodPost && request.URL.Path == "/v1.55/images/create":
			if request.URL.Query().Get("fromImage") != "docker.io/library/python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
				t.Errorf("fromImage = %q", request.URL.Query().Get("fromImage"))
			}
			imagePresent = true
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte("{\"status\":\"done\"}\n"))
		case request.Method == http.MethodGet && (request.URL.Path == "/v1.55/containers/example/json" || request.URL.Path == "/v1.55/containers/container-id/json"):
			if !containerPresent {
				writeJSON(response, http.StatusNotFound, map[string]string{"message": "not found"})
				return
			}
			writeJSON(response, http.StatusOK, map[string]any{
				"Id": "container-id", "Config": map[string]any{"Labels": map[string]string{"owner": "test"}},
				"State":           map[string]any{"Running": true},
				"NetworkSettings": map[string]any{"Ports": map[string]any{"8080/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": strconv.Itoa(servicePort)}}}},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/v1.55/containers/create":
			var body struct {
				HostConfig struct {
					PortBindings map[string][]map[string]string `json:"PortBindings"`
					SecurityOpt  []string                       `json:"SecurityOpt"`
				} `json:"HostConfig"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.HostConfig.PortBindings["8080/tcp"][0]["HostIp"] != "127.0.0.1" || len(body.HostConfig.SecurityOpt) == 0 {
				t.Errorf("unsafe container host config: %#v", body.HostConfig)
			}
			containerPresent = true
			writeJSON(response, http.StatusCreated, map[string]string{"Id": "container-id"})
		case request.Method == http.MethodPost && request.URL.Path == "/v1.55/containers/container-id/start":
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", request.Method, request.URL.String())
			writeJSON(response, http.StatusNotFound, map[string]string{"message": "unexpected"})
		}
	}))
	defer server.Close()
	client, err := NewEngineClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	reference := "docker.io/library/python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := client.EnsureImage(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
	observation, err := client.EnsureContainer(context.Background(), ContainerCreate{
		Name: "example", Image: reference, Network: "example-network", Labels: map[string]string{"owner": "test"},
		Published: []PublishedPort{{ContainerPort: 8080, Protocol: "tcp", HostIP: "127.0.0.1"}}, CPUMillis: 250, MemoryBytes: 128 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Ports["8080/tcp"] != uint16(servicePort) {
		t.Fatalf("observed ports = %#v", observation.Ports)
	}
}

func TestEngineRejectsRemotePlaintextEndpoint(t *testing.T) {
	if _, err := NewEngineClient("tcp://192.0.2.10:2375"); err == nil {
		t.Fatal("remote plaintext Docker endpoint was accepted")
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
