package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

type Client struct {
	origin              *url.URL
	expectedAgentDigest string
	httpClient          *http.Client
}

func NewClient(origin string, serverCAPEM, clientCertificatePEM, clientKeyPEM []byte, expectedAgentDigest string) (*Client, error) {
	return newClient(origin, serverCAPEM, clientCertificatePEM, clientKeyPEM, expectedAgentDigest, false)
}

func newClient(origin string, serverCAPEM, clientCertificatePEM, clientKeyPEM []byte, expectedAgentDigest string, allowTestLoopback bool) (*Client, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" || !digestPattern.MatchString(expectedAgentDigest) {
		return nil, errors.New("qualification probe client configuration is invalid")
	}
	agentAddress, err := netip.ParseAddrPort(parsed.Host)
	validAddress := err == nil && agentAddress.Addr().Is4() && agentAddress.Port() >= 1024 &&
		(agentAddress.Addr().IsGlobalUnicast() && !agentAddress.Addr().IsLoopback() || allowTestLoopback && agentAddress.Addr().IsLoopback())
	if !validAddress {
		return nil, errors.New("qualification probe agent origin must use an exact non-loopback IPv4 address and unprivileged port")
	}
	parsed.Path, parsed.RawPath = "", ""
	serverRoots := x509.NewCertPool()
	if !serverRoots.AppendCertsFromPEM(serverCAPEM) {
		return nil, errors.New("qualification probe server CA is invalid")
	}
	certificate, err := tls.X509KeyPair(clientCertificatePEM, clientKeyPEM)
	if err != nil {
		return nil, errors.New("qualification probe client identity is invalid")
	}
	transport := &http.Transport{
		Proxy: nil, ForceAttemptHTTP2: false, DisableCompression: true,
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 10 * time.Second}).DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: serverRoots, Certificates: []tls.Certificate{certificate}, NextProtos: []string{"http/1.1"},
		},
		MaxIdleConns: 1, MaxIdleConnsPerHost: 1, MaxConnsPerHost: 1, IdleConnTimeout: 5 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
	}
	return &Client{
		origin: parsed, expectedAgentDigest: expectedAgentDigest,
		httpClient: &http.Client{
			Transport: transport, Timeout: 3 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("qualification probe redirects are disabled")
			},
		},
	}, nil
}

func (client *Client) Run(ctx context.Context, input Request) (Response, string, error) {
	if client == nil || client.origin == nil || client.httpClient == nil || ValidateRequest(input) != nil {
		return Response{}, "", errors.New("qualification probe client or request is invalid")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Response{}, "", err
	}
	endpoint := *client.origin
	endpoint.Path = Path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return Response{}, "", errors.New("construct qualification probe request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return Response{}, "", errors.New("qualification probe agent is unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > MaxBodyBytes || response.StatusCode != http.StatusOK {
		return Response{}, "", fmt.Errorf("qualification probe agent returned HTTP %d or invalid bounded output", response.StatusCode)
	}
	result, err := DecodeResponse(body)
	if err != nil || ValidateResponse(result, input, client.expectedAgentDigest) != nil {
		return Response{}, "", errors.New("qualification probe agent returned invalid bound evidence")
	}
	digest, err := TranscriptDigest(input, result)
	if err != nil {
		return Response{}, "", errors.New("qualification probe transcript could not be canonicalized")
	}
	return result, digest, nil
}

func (client *Client) CloseIdleConnections() {
	if client != nil && client.httpClient != nil {
		client.httpClient.CloseIdleConnections()
	}
}
