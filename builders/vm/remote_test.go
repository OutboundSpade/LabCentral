package vm

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"crypto/tls"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func TestWorkerTransportRequiresMutualTLSAndInstalledKind(t *testing.T) {
	service, _, _ := workerFixture(t)
	serverFiles, clientFiles, caBytes := workerTLSFixture(t, t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeWorker(ctx, address, serverFiles, service) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	relay, err := NewRelay(address, clientFiles, "ansible")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	healthy := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		probe, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
		out, err := relay.Health(probe, &wire.HealthRequest{})
		stop()
		if err == nil && out.Status == wire.HealthResponse_STATUS_HEALTHY {
			healthy = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("authorized relay could not authenticate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caBytes})) {
		t.Fatal("invalid test CA")
	}
	connection, err := grpc.NewClient("passthrough:///"+address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "worker.example"})))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	probe, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err = wire.NewPluginRuntimeServiceClient(connection).Health(probe, &wire.HealthRequest{}); err == nil {
		t.Fatal("worker accepted client without a certificate")
	}
	if _, err = relay.Validate(context.Background(), &wire.BuilderServiceValidateRequest{Recipe: &wire.BuildRecipe{Configuration: &wire.ExtensionConfiguration{Schema: &wire.SchemaVersion{Name: DockerfileSchema, Major: 1}}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("installed Ansible package accepted Dockerfile schema", err)
	}
}

func workerTLSFixture(t *testing.T, dir string) (TLSFiles, TLSFiles, []byte) {
	t.Helper()
	now := time.Now()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "builder-test-ca"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caBytes}), 0600)
	issue := func(name string, usage x509.ExtKeyUsage) TLSFiles {
		public, private, _ := ed25519.GenerateKey(rand.Reader)
		template := &x509.Certificate{SerialNumber: big.NewInt(int64(len(name) + 2)), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		certificate, err := x509.CreateCertificate(rand.Reader, template, ca, public, key)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		out := TLSFiles{CA: caPath, Certificate: filepath.Join(dir, name+".pem"), Key: filepath.Join(dir, name+".key"), ServerName: "worker.example"}
		os.WriteFile(out.Certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0600)
		os.WriteFile(out.Key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0600)
		return out
	}
	serverFiles := issue("worker.example", x509.ExtKeyUsageServerAuth)
	clientFiles := issue("core-relay", x509.ExtKeyUsageClientAuth)
	return serverFiles, clientFiles, caBytes
}
