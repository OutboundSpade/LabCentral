package proxmox

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
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	defaultRequestTimeout  = 10 * time.Second
	defaultResponseLimit   = int64(4 << 20)
	maximumResponseLimit   = int64(16 << 20)
	maximumConfigBytes     = 1 << 20
	maximumCredentialBytes = 8 << 10
	maximumNodes           = 64
)

var tokenIDPattern = regexp.MustCompile(`^[^\s/:@!]{1,128}@[A-Za-z][A-Za-z0-9._-]{0,63}![A-Za-z][A-Za-z0-9._-]{0,127}$`)
var tokenSecretPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,512}$`)

type DiscoveryClient interface {
	Discover(context.Context) (Inventory, error)
	CloseIdleConnections()
}

type ClientFactory interface {
	New(configEnvelope, credentialEnvelope []byte) (DiscoveryClient, error)
}

type ClientFactoryFunc func(configEnvelope, credentialEnvelope []byte) (DiscoveryClient, error)

func (factory ClientFactoryFunc) New(configEnvelope, credentialEnvelope []byte) (DiscoveryClient, error) {
	return factory(configEnvelope, credentialEnvelope)
}

type Client struct {
	endpoint         *url.URL
	tokenID          string
	tokenSecret      string
	maxResponseBytes int64
	httpClient       *http.Client
}

type APIStatusError struct {
	Path       string
	StatusCode int
}

func (err *APIStatusError) Error() string {
	return fmt.Sprintf("Proxmox API GET %s returned HTTP %d", err.Path, err.StatusCode)
}

func NewClientFromEnvelopes(configEnvelope, credentialEnvelope []byte) (*Client, error) {
	var config TargetConfig
	if err := decodeEnvelope(configEnvelope, maximumConfigBytes, &config); err != nil {
		return nil, fmt.Errorf("invalid Proxmox target configuration: %w", err)
	}
	var credentials APITokenCredentials
	if err := decodeEnvelope(credentialEnvelope, maximumCredentialBytes, &credentials); err != nil {
		return nil, fmt.Errorf("invalid Proxmox API token credentials")
	}
	return NewClient(config, credentials)
}

func NewClient(config TargetConfig, credentials APITokenCredentials) (*Client, error) {
	endpoint, err := validateEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	if !tokenIDPattern.MatchString(credentials.TokenID) {
		return nil, fmt.Errorf("Proxmox tokenID must use USER@REALM!TOKENID syntax")
	}
	if !tokenSecretPattern.MatchString(credentials.TokenSecret) {
		return nil, fmt.Errorf("Proxmox tokenSecret has an invalid format")
	}
	timeout := defaultRequestTimeout
	if config.RequestTimeoutSeconds != 0 {
		if config.RequestTimeoutSeconds > 30 {
			return nil, fmt.Errorf("Proxmox requestTimeoutSeconds must not exceed 30")
		}
		timeout = time.Duration(config.RequestTimeoutSeconds) * time.Second
	}
	limit := config.MaxResponseBytes
	if limit == 0 {
		limit = defaultResponseLimit
	}
	if limit < 1024 || limit > maximumResponseLimit {
		return nil, fmt.Errorf("Proxmox maxResponseBytes must be between 1024 and %d", maximumResponseLimit)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if config.CACertificatePEM != "" && !roots.AppendCertsFromPEM([]byte(config.CACertificatePEM)) {
		return nil, fmt.Errorf("Proxmox caCertificatePEM contains no valid certificates")
	}
	transport := &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
	}
	return &Client{
		endpoint: endpoint, tokenID: credentials.TokenID, tokenSecret: credentials.TokenSecret,
		maxResponseBytes: limit,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("Proxmox API redirects are disabled")
			},
		},
	}, nil
}

func validateEndpoint(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	endpoint, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("parse Proxmox endpoint: %w", err)
	}
	if endpoint.Scheme != "https" {
		return nil, fmt.Errorf("Proxmox endpoint must use https")
	}
	if endpoint.Host == "" || endpoint.Hostname() == "" {
		return nil, fmt.Errorf("Proxmox endpoint must include a host")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, fmt.Errorf("Proxmox endpoint must be an HTTPS origin without credentials, path, query, or fragment")
	}
	endpoint.Path = ""
	endpoint.RawPath = ""
	return endpoint, nil
}

func decodeEnvelope(contents []byte, limit int, destination any) error {
	if len(contents) == 0 {
		return fmt.Errorf("envelope is required")
	}
	if len(contents) > limit {
		return fmt.Errorf("envelope exceeds %d bytes", limit)
	}
	if err := rejectDuplicateTopLevelKeys(contents); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON envelope must contain exactly one value")
	}
	return nil
}

func rejectDuplicateTopLevelKeys(contents []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode JSON envelope: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("JSON envelope must be an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode JSON envelope: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("JSON envelope contained a non-string key")
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("JSON envelope contains duplicate field %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode JSON envelope: %w", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode JSON envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON envelope must contain exactly one value")
	}
	return nil
}

func (client *Client) CloseIdleConnections() {
	client.httpClient.CloseIdleConnections()
}

type apiResponse struct {
	Data json.RawMessage `json:"data"`
}

func getAPI[T any](ctx context.Context, client *Client, path string, query url.Values) (T, error) {
	var zero T
	requestURL := *client.endpoint
	requestURL.Path = "/api2/json" + path
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return zero, fmt.Errorf("create Proxmox API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "PVEAPIToken="+client.tokenID+"="+client.tokenSecret)
	request.Header.Set("User-Agent", "LabCentral-Proxmox/"+PluginVersion)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return zero, fmt.Errorf("Proxmox API GET %s failed: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return zero, &APIStatusError{Path: path, StatusCode: response.StatusCode}
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, client.maxResponseBytes+1))
	if err != nil {
		return zero, fmt.Errorf("read Proxmox API GET %s: %w", path, err)
	}
	if int64(len(contents)) > client.maxResponseBytes {
		return zero, fmt.Errorf("Proxmox API GET %s response exceeds %d bytes", path, client.maxResponseBytes)
	}
	var envelope apiResponse
	if err := json.Unmarshal(contents, &envelope); err != nil {
		return zero, fmt.Errorf("decode Proxmox API GET %s response: %w", path, err)
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return zero, fmt.Errorf("decode Proxmox API GET %s response: data is required", path)
	}
	var result T
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return zero, fmt.Errorf("decode Proxmox API GET %s data: %w", path, err)
	}
	return result, nil
}

type apiBool bool

func (value *apiBool) UnmarshalJSON(contents []byte) error {
	text := strings.TrimSpace(string(contents))
	switch text {
	case "true", "1", `"1"`:
		*value = true
		return nil
	case "false", "0", `"0"`, "null":
		*value = false
		return nil
	default:
		return fmt.Errorf("expected Proxmox boolean")
	}
}
