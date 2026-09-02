package probe

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeClientUsesPinnedTLSAndBoundedTypedResponse(t *testing.T) {
	handler, err := NewHandler(testDigest("4"))
	if err != nil {
		t.Fatal(err)
	}
	handler.Dial = func(context.Context, string) error { return nil }
	clientCertPEM, clientKeyPEM := testClientIdentity(t)
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(clientCertPEM) {
		t.Fatal("could not trust test client certificate")
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientRoots,
		NextProtos: []string{"http/1.1"},
	}
	server.StartTLS()
	defer server.Close()
	serverCAPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if _, err := NewClient(server.URL, serverCAPEM, clientCertPEM, clientKeyPEM, testDigest("4")); err == nil {
		t.Fatal("production client accepted a loopback probe-agent origin")
	}
	if _, err := NewClient("https://probe.invalid:9443", serverCAPEM, clientCertPEM, clientKeyPEM, testDigest("4")); err == nil {
		t.Fatal("production client accepted a DNS probe-agent origin")
	}
	client, err := newClient(server.URL, serverCAPEM, clientCertPEM, clientKeyPEM, testDigest("4"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	response, transcript, err := client.Run(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.AgentImageDigest != testDigest("4") || !strings.HasPrefix(transcript, "sha256:") {
		t.Fatalf("response = %#v, transcript = %q", response, transcript)
	}

	t.Run("wrong server trust fails closed", func(t *testing.T) {
		wrongCAPEM, _ := testClientIdentity(t)
		wrong, err := newClient(server.URL, wrongCAPEM, clientCertPEM, clientKeyPEM, testDigest("4"), true)
		if err != nil {
			t.Fatal(err)
		}
		defer wrong.CloseIdleConnections()
		if _, _, err := wrong.Run(context.Background(), testRequest()); err == nil {
			t.Fatal("untrusted probe server was accepted")
		}
	})

	t.Run("wrong client identity fails closed", func(t *testing.T) {
		wrongClientCert, wrongClientKey := testClientIdentity(t)
		wrong, err := newClient(server.URL, serverCAPEM, wrongClientCert, wrongClientKey, testDigest("4"), true)
		if err != nil {
			t.Fatal(err)
		}
		defer wrong.CloseIdleConnections()
		if _, _, err := wrong.Run(context.Background(), testRequest()); err == nil {
			t.Fatal("untrusted qualification runner was accepted")
		}
	})

	t.Run("redirect and upstream body are not exposed", func(t *testing.T) {
		redirect := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Location", server.URL+Path)
			writer.WriteHeader(http.StatusTemporaryRedirect)
			_, _ = writer.Write([]byte("probe-client-canary"))
		}))
		defer redirect.Close()
		redirectCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: redirect.Certificate().Raw})
		redirectClient, err := newClient(redirect.URL, redirectCA, clientCertPEM, clientKeyPEM, testDigest("4"), true)
		if err != nil {
			t.Fatal(err)
		}
		defer redirectClient.CloseIdleConnections()
		_, _, err = redirectClient.Run(context.Background(), testRequest())
		if err == nil || strings.Contains(err.Error(), "canary") {
			t.Fatalf("redirect result = %v", err)
		}
	})
}

func testClientIdentity(t *testing.T) ([]byte, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "qualification-runner"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
