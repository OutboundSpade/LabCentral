package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestContractTESTPROXMOX026GuestProbeProtocolIsClosedBoundedAndRedacted(t *testing.T) {
	request := testRequest()
	handler, err := NewHandler(testDigest("1"))
	if err != nil {
		t.Fatal(err)
	}
	times := []time.Time{
		time.Date(2026, 9, 2, 20, 30, 0, 0, time.UTC),
		time.Date(2026, 9, 2, 20, 30, 1, 0, time.UTC),
	}
	handler.Now = func() time.Time {
		value := times[0]
		if len(times) > 1 {
			times = times[1:]
		}
		return value
	}
	handler.Dial = func(_ context.Context, address string) error {
		if address == request.Targets[0].Address {
			return nil
		}
		return errors.New("unreachable")
	}
	response, err := handler.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateResponse(response, request, testDigest("1")); err != nil {
		t.Fatal(err)
	}
	if response.Results[0].Successes != request.Attempts || response.Results[1].Successes != 0 || response.Results[2].Successes != 0 {
		t.Fatalf("probe observations = %#v", response.Results)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range request.Targets {
		if bytes.Contains(encoded, []byte(target.Address)) {
			t.Fatalf("raw target %q leaked into response", target.Address)
		}
	}
	firstDigest, err := TranscriptDigest(request, response)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := TranscriptDigest(request, response)
	if err != nil || firstDigest != secondDigest || !strings.HasPrefix(firstDigest, "sha256:") {
		t.Fatalf("transcript digests = %q %q, %v", firstDigest, secondDigest, err)
	}

	t.Run("response is nonce target and image bound", func(t *testing.T) {
		tampered := response
		tampered.Nonce = strings.Repeat("b", 64)
		if err := ValidateResponse(tampered, request, testDigest("1")); err == nil {
			t.Fatal("tampered nonce was accepted")
		}
		tampered = response
		tampered.Results = append([]Observation(nil), response.Results...)
		tampered.Results[0].EndpointDigest = testDigest("f")
		if err := ValidateResponse(tampered, request, testDigest("1")); err == nil {
			t.Fatal("tampered endpoint binding was accepted")
		}
	})

	t.Run("HTTP boundary rejects duplicate unknown and oversized input", func(t *testing.T) {
		handler, err := NewHandler(testDigest("2"))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(request)
		for name, body := range map[string][]byte{
			"duplicate": bytes.Replace(raw, []byte(`"runID":`), []byte(`"runID":"duplicate","runID":`), 1),
			"unknown":   bytes.Replace(raw, []byte(`{"schemaVersion":`), []byte(`{"command":"shell","schemaVersion":`), 1),
			"oversized": bytes.Repeat([]byte("x"), MaxBodyBytes+1),
		} {
			t.Run(name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				httpRequest := httptest.NewRequest(http.MethodPost, Path, bytes.NewReader(body))
				httpRequest.Header.Set("Content-Type", "application/json")
				handler.ServeHTTP(recorder, httpRequest)
				if recorder.Code != http.StatusBadRequest || strings.Contains(recorder.Body.String(), "shell") {
					t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
				}
			})
		}
	})

	t.Run("handler permits only one bounded run at a time", func(t *testing.T) {
		handler, err := NewHandler(testDigest("3"))
		if err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		handler.Dial = func(context.Context, string) error {
			once.Do(func() { close(entered) })
			<-release
			return nil
		}
		raw, _ := json.Marshal(request)
		firstDone := make(chan struct{})
		go func() {
			defer close(firstDone)
			recorder := httptest.NewRecorder()
			httpRequest := httptest.NewRequest(http.MethodPost, Path, bytes.NewReader(raw))
			httpRequest.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(recorder, httpRequest)
		}()
		<-entered
		second := httptest.NewRecorder()
		secondRequest := httptest.NewRequest(http.MethodPost, Path, bytes.NewReader(raw))
		secondRequest.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(second, secondRequest)
		if second.Code != http.StatusConflict {
			t.Fatalf("concurrent request status = %d", second.Code)
		}
		close(release)
		<-firstDone
	})

	t.Run("target and effort bounds reject scanner-shaped requests", func(t *testing.T) {
		invalid := request
		invalid.Attempts = 11
		if err := ValidateRequest(invalid); err == nil {
			t.Fatal("excessive attempts were accepted")
		}
		invalid = request
		invalid.Targets = append(invalid.Targets, Target{Kind: "extra", Address: "10.22.0.1:1"})
		if err := ValidateRequest(invalid); err == nil {
			t.Fatal("extra target was accepted")
		}
		invalid = request
		invalid.Targets = append([]Target(nil), request.Targets...)
		invalid.Targets[1].Address = "127.0.0.1:8006"
		if err := ValidateRequest(invalid); err == nil {
			t.Fatal("loopback target was accepted")
		}
	})
}

func testRequest() Request {
	return Request{
		SchemaVersion: RequestSchema,
		RunID:         "qualification.probe-001",
		Nonce:         strings.Repeat("a", 64),
		Attempts:      3,
		TimeoutMillis: 500,
		Targets: []Target{
			{Kind: "intended_segment", Address: "10.20.0.11:9443"},
			{Kind: "management", Address: "10.0.0.2:8006"},
			{Kind: "cross_instance", Address: "10.21.0.10:9443"},
		},
	}
}

func testDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
