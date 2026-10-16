package aws

import (
	"context"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/smithy-go"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEC2DenialVersusAmbiguousFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		err   error
		calls int
	}{
		{"authorization", &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "denied"}, 2},
		{"authentication", &smithy.GenericAPIError{Code: "AuthFailure", Message: "denied"}, 2},
		{"timeout", context.DeadlineExceeded, 1},
		{"transport", errors.New("response lost"), 1},
		{"service", &smithy.GenericAPIError{Code: "InternalError", Message: "unknown outcome"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			j := journal.Journal{Root: t.TempDir()}
			calls := 0
			create := func() (string, error) { calls++; return "", test.err }
			find := func() (string, bool, error) { return "", false, nil }
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := doEC2(context.Background(), j, "network", create, find); err == nil {
					t.Fatal("failure disappeared")
				}
			}
			if calls != test.calls {
				t.Fatalf("create called %d times, want %d", calls, test.calls)
			}
		})
	}
}

// SDK replay must not precede journal reconciliation for a non-idempotent call.
func TestConfiguredEC2ClientDoesNotRetryLostMutation(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "example-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "example-secret")
	t.Setenv("AWS_MAX_ATTEMPTS", "9")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer server.Close()
	t.Setenv("AWS_ENDPOINT_URL_EC2", server.URL)
	api, _, err := client(context.Background(), lc.Environment{AWS: lc.AWSConfig{Region: "us-east-1"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.CreateVpc(context.Background(), &ec2.CreateVpcInput{CidrBlock: aw.String("10.77.0.0/16")}, noCreateRetries)
	if err == nil || requests.Load() != 1 {
		t.Fatal("SDK repeated an uncertain mutation", requests.Load(), err)
	}
}

func TestConfiguredEC2ReadsRetryThrottleAndRemainBounded(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			t.Setenv("AWS_ACCESS_KEY_ID", "example")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "example")
			t.Setenv("AWS_MAX_ATTEMPTS", "9")
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				if r.Form.Get("Action") != "DescribeVolumes" {
					t.Errorf("unexpected native action")
				}
				n := calls.Add(1)
				w.Header().Set("Content-Type", "text/xml")
				if n == 1 || !recover {
					w.WriteHeader(503)
					io.WriteString(w, `<Response><Errors><Error><Code>RequestLimitExceeded</Code><Message>throttled</Message></Error></Errors></Response>`)
					return
				}
				io.WriteString(w, `<DescribeVolumesResponse><volumeSet/></DescribeVolumesResponse>`)
			}))
			defer server.Close()
			t.Setenv("AWS_ENDPOINT_URL_EC2", server.URL)
			api, _, err := client(context.Background(), lc.Environment{AWS: lc.AWSConfig{Region: "us-east-1"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = api.DescribeVolumes(context.Background(), &ec2.DescribeVolumesInput{})
			if recover && (err != nil || calls.Load() != 2) {
				t.Fatal("read did not retry", err, calls.Load())
			}
			if !recover && (err == nil || calls.Load() != 3) {
				t.Fatal("read retries not bounded", err, calls.Load())
			}
		})
	}
}
