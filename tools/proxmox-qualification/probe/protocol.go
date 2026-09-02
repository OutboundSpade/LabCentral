// Package probe implements the bounded qualification-only guest probe
// protocol. It has no shell, command execution, file access, or provider API.
package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OutboundSpade/LabCentral/internal/render"
)

const (
	RequestSchema  = "labcentral.proxmox-qualification-probe-request/v1"
	ResponseSchema = "labcentral.proxmox-qualification-probe-response/v1"
	Path           = "/v1/probes"
	MaxBodyBytes   = 16 << 10
)

var (
	stableIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	noncePattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

var requiredKinds = []string{"intended_segment", "management", "cross_instance"}

type Request struct {
	SchemaVersion string   `json:"schemaVersion"`
	RunID         string   `json:"runID"`
	Nonce         string   `json:"nonce"`
	Attempts      uint32   `json:"attempts"`
	TimeoutMillis uint32   `json:"timeoutMillis"`
	Targets       []Target `json:"targets"`
}

type Target struct {
	Kind    string `json:"kind"`
	Address string `json:"address"`
}

type Response struct {
	SchemaVersion    string        `json:"schemaVersion"`
	RunID            string        `json:"runID"`
	Nonce            string        `json:"nonce"`
	AgentImageDigest string        `json:"agentImageDigest"`
	StartedAt        string        `json:"startedAt"`
	CompletedAt      string        `json:"completedAt"`
	Results          []Observation `json:"results"`
}

type Observation struct {
	Kind           string `json:"kind"`
	EndpointDigest string `json:"endpointDigest"`
	Attempts       uint32 `json:"attempts"`
	Successes      uint32 `json:"successes"`
}

type Dial func(context.Context, string) error

type Handler struct {
	AgentImageDigest string
	Dial             Dial
	Now              func() time.Time

	active atomic.Bool
}

func NewHandler(agentImageDigest string) (*Handler, error) {
	if !digestPattern.MatchString(agentImageDigest) {
		return nil, errors.New("probe agent image digest is invalid")
	}
	return &Handler{AgentImageDigest: agentImageDigest}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.URL.Path != Path || request.URL.RawQuery != "" || request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
		writeFailure(writer, http.StatusNotFound, "unsupported_request")
		return
	}
	if !handler.active.CompareAndSwap(false, true) {
		writeFailure(writer, http.StatusConflict, "probe_in_progress")
		return
	}
	defer handler.active.Store(false)
	body, err := io.ReadAll(io.LimitReader(request.Body, MaxBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > MaxBodyBytes {
		writeFailure(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	var input Request
	if err := decodeClosed(body, &input); err != nil || ValidateRequest(input) != nil {
		writeFailure(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := handler.Run(request.Context(), input)
	if err != nil {
		writeFailure(writer, http.StatusRequestTimeout, "probe_incomplete")
		return
	}
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler *Handler) Run(ctx context.Context, request Request) (Response, error) {
	if handler == nil || !digestPattern.MatchString(handler.AgentImageDigest) || ValidateRequest(request) != nil {
		return Response{}, errors.New("probe request or handler is invalid")
	}
	dial := handler.Dial
	if dial == nil {
		dialer := &net.Dialer{}
		dial = func(ctx context.Context, address string) error {
			connection, err := dialer.DialContext(ctx, "tcp", address)
			if err == nil {
				_ = connection.Close()
			}
			return err
		}
	}
	now := handler.Now
	if now == nil {
		now = time.Now
	}
	startedAt := canonicalTime(now())
	results := make([]Observation, 0, len(request.Targets))
	for _, target := range request.Targets {
		observation := Observation{Kind: target.Kind, EndpointDigest: endpointDigest(target.Address), Attempts: request.Attempts}
		for attempt := uint32(0); attempt < request.Attempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return Response{}, err
			}
			attemptContext, cancel := context.WithTimeout(ctx, time.Duration(request.TimeoutMillis)*time.Millisecond)
			err := dial(attemptContext, target.Address)
			cancel()
			if err == nil {
				observation.Successes++
			}
		}
		results = append(results, observation)
	}
	completedAt := canonicalTime(now())
	if completedAt < startedAt {
		completedAt = startedAt
	}
	return Response{
		SchemaVersion: ResponseSchema, RunID: request.RunID, Nonce: request.Nonce,
		AgentImageDigest: handler.AgentImageDigest, StartedAt: startedAt, CompletedAt: completedAt, Results: results,
	}, nil
}

func ValidateRequest(request Request) error {
	if request.SchemaVersion != RequestSchema || !stableIDPattern.MatchString(request.RunID) || !noncePattern.MatchString(request.Nonce) || request.Attempts < 3 || request.Attempts > 10 || request.TimeoutMillis < 100 || request.TimeoutMillis > 5000 || len(request.Targets) != len(requiredKinds) {
		return errors.New("qualification probe request is invalid")
	}
	seen := make(map[netip.AddrPort]bool, len(request.Targets))
	for index, target := range request.Targets {
		address, err := netip.ParseAddrPort(target.Address)
		if err != nil || target.Kind != requiredKinds[index] || !address.Addr().Is4() || !address.Addr().IsGlobalUnicast() || address.Addr().IsLoopback() || address.Port() == 0 || seen[address] {
			return errors.New("qualification probe target is invalid")
		}
		seen[address] = true
	}
	return nil
}

func ValidateResponse(response Response, request Request, expectedAgentDigest string) error {
	if ValidateRequest(request) != nil || response.SchemaVersion != ResponseSchema || response.RunID != request.RunID || response.Nonce != request.Nonce || response.AgentImageDigest != expectedAgentDigest || !digestPattern.MatchString(response.AgentImageDigest) || len(response.Results) != len(requiredKinds) {
		return errors.New("qualification probe response binding is invalid")
	}
	started, err := time.Parse(time.RFC3339, response.StartedAt)
	if err != nil || started.Format(time.RFC3339) != response.StartedAt || started.Location() != time.UTC {
		return errors.New("qualification probe response start time is invalid")
	}
	completed, err := time.Parse(time.RFC3339, response.CompletedAt)
	if err != nil || completed.Format(time.RFC3339) != response.CompletedAt || completed.Location() != time.UTC || completed.Before(started) {
		return errors.New("qualification probe response completion time is invalid")
	}
	for index, result := range response.Results {
		if result.Kind != requiredKinds[index] || result.EndpointDigest != endpointDigest(request.Targets[index].Address) || result.Attempts != request.Attempts || result.Successes > result.Attempts {
			return errors.New("qualification probe observation is invalid")
		}
	}
	return nil
}

func DecodeResponse(input []byte) (Response, error) {
	var result Response
	if err := decodeClosed(input, &result); err != nil {
		return Response{}, err
	}
	return result, nil
}

func TranscriptDigest(request Request, response Response) (string, error) {
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	requestValue, err := render.DecodeJSON(requestJSON)
	if err != nil {
		return "", err
	}
	responseValue, err := render.DecodeJSON(responseJSON)
	if err != nil {
		return "", err
	}
	canonicalRequest, err := render.Canonicalize(requestValue)
	if err != nil {
		return "", err
	}
	canonicalResponse, err := render.Canonicalize(responseValue)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(RequestSchema))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonicalRequest)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonicalResponse)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func decodeClosed(input []byte, destination any) error {
	if len(input) == 0 || len(input) > MaxBodyBytes {
		return errors.New("probe JSON size is invalid")
	}
	value, err := render.DecodeJSON(input)
	if err != nil {
		return fmt.Errorf("probe JSON is invalid: %w", err)
	}
	canonical, err := render.Canonicalize(value)
	if err != nil {
		return fmt.Errorf("probe JSON is invalid: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("probe JSON does not match its closed schema: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("probe JSON contains multiple values")
	}
	return nil
}

func endpointDigest(address string) string {
	hash := sha256.Sum256([]byte("labcentral.proxmox-probe-endpoint/v1\x00" + address))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func canonicalTime(value time.Time) string {
	return value.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func writeFailure(writer http.ResponseWriter, status int, code string) {
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, `{"error":"`+strings.ReplaceAll(code, `"`, "")+`"}`+"\n")
}
