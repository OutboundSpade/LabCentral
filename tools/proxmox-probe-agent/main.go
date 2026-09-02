// Command proxmox-probe-agent serves the qualification-only, mTLS-protected
// guest-origin TCP probe endpoint. It does not execute commands or call a
// provider API and is not part of LabCentral production release artifacts.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/OutboundSpade/LabCentral/tools/proxmox-qualification/probe"
)

type configuration struct {
	listen           string
	serverCert       string
	serverKey        string
	clientCA         string
	agentImageDigest string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	config, err := parseConfiguration(arguments)
	if err != nil {
		return err
	}
	certificatePEM, err := readAgentFile(config.serverCert, 64<<10, false)
	if err != nil {
		return errors.New("probe agent server certificate is unavailable")
	}
	privateKeyPEM, err := readAgentFile(config.serverKey, 64<<10, true)
	if err != nil {
		return errors.New("probe agent server key is unavailable")
	}
	defer clear(privateKeyPEM)
	clientCAPEM, err := readAgentFile(config.clientCA, 64<<10, false)
	if err != nil {
		return errors.New("probe agent client CA is unavailable")
	}
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		return errors.New("probe agent TLS identity is invalid")
	}
	clientRoots := x509.NewCertPool()
	if !clientRoots.AppendCertsFromPEM(clientCAPEM) {
		return errors.New("probe agent client CA is invalid")
	}
	handler, err := probe.NewHandler(config.agentImageDigest)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.listen)
	if err != nil {
		return errors.New("probe agent could not bind its configured address")
	}
	defer listener.Close()
	tlsListener := tls.NewListener(listener, &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientRoots,
		NextProtos:   []string{"http/1.1"},
	})
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 3 * time.Minute, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8 << 10,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- server.Serve(tlsListener) }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("probe agent server stopped unexpectedly")
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return errors.New("probe agent shutdown was incomplete")
		}
		return nil
	}
}

func parseConfiguration(arguments []string) (configuration, error) {
	flags := flag.NewFlagSet("proxmox-probe-agent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := configuration{}
	flags.StringVar(&config.listen, "listen", "", "exact non-loopback guest IPv4 address and unprivileged port")
	flags.StringVar(&config.serverCert, "server-cert", "", "PEM server certificate file")
	flags.StringVar(&config.serverKey, "server-key", "", "mode-protected PEM server key file")
	flags.StringVar(&config.clientCA, "client-ca", "", "PEM CA for required client certificates")
	flags.StringVar(&config.agentImageDigest, "agent-image-digest", "", "sha256 digest of the qualification guest image")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return configuration{}, errors.New("invalid probe agent arguments")
	}
	address, err := netip.ParseAddrPort(config.listen)
	if err != nil || !address.Addr().Is4() || !address.Addr().IsGlobalUnicast() || address.Addr().IsLoopback() || address.Port() < 1024 {
		return configuration{}, errors.New("probe agent listen address must be an exact non-loopback IPv4 address and unprivileged port")
	}
	paths := []string{config.serverCert, config.serverKey, config.clientCA}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || seen[path] {
			return configuration{}, errors.New("probe agent TLS paths must be absolute, clean, non-root, and distinct")
		}
		seen[path] = true
	}
	if _, err := probe.NewHandler(config.agentImageDigest); err != nil {
		return configuration{}, err
	}
	return config, nil
}

func readAgentFile(path string, maximum int64, private bool) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > maximum {
		return nil, errors.New("input is not a bounded non-symlink regular file")
	}
	if private && before.Mode().Perm()&0o077 != 0 || !private && before.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("input permissions are unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || private && after.Mode().Perm()&0o077 != 0 || !private && after.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("input identity or permissions changed")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(contents) == 0 || int64(len(contents)) > maximum {
		clear(contents)
		return nil, errors.New("input could not be read within its bound")
	}
	return contents, nil
}
