package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/nativeproxy"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/smithy-go"
)

func throttleError(code string) error {
	return fmt.Errorf("wrapped SDK error: %w", &smithy.OperationError{ServiceID: "EC2", OperationName: "CreateVpcEndpoint", Err: &smithy.GenericAPIError{Code: code}})
}

func TestEC2MutationRetryPolicy(t *testing.T) {
	for _, code := range []string{"RequestLimitExceeded", "Throttling", "ThrottlingException", "EC2ThrottledException"} {
		for _, failures := range []int{1, 3, 5, 6} {
			t.Run(fmt.Sprintf("%s/%d", code, failures), func(t *testing.T) {
				j := journal.Journal{Root: t.TempDir()}
				calls, waits := 0, 0
				result, err := journal.Do(context.Background(), j, "create", func() (string, error) {
					return retryEC2Mutation(context.Background(), func() (string, error) {
						calls++
						intent, e := j.HasIntent("create")
						var saved string
						if !intent || e != nil || j.Load("create", &saved) == nil {
							t.Fatal("attempt without pending durable intent", intent, e, saved)
						}
						if calls <= failures {
							return "not a successful result", throttleError(code)
						}
						return "created", nil
					}, func(_ context.Context, delay time.Duration) error {
						ceiling := min(time.Second<<waits, 16*time.Second)
						if delay < 0 || delay >= ceiling {
							t.Fatal("backoff outside full-jitter window", delay, ceiling)
						}
						waits++
						return nil
					})
				}, func() (string, bool, error) { t.Fatal("unexpected discovery"); return "", false, nil })
				if calls != min(failures+1, 6) || waits != min(failures, 5) {
					t.Fatal("incorrect attempt cap", calls, waits)
				}
				var saved string
				if failures < 6 {
					if err != nil || result != "created" || j.Load("create", &saved) != nil || saved != result {
						t.Fatal("successful result not checkpointed", result, saved, err)
					}
				} else {
					if err == nil || !strings.Contains(err.Error(), "after 6 attempts") || !strings.Contains(err.Error(), "retry the operation") || strings.Contains(err.Error(), "recovery_required") || !throttledEC2Error(err) {
						t.Fatal("exhaustion not a clear retryable rejection", err)
					}
					intent, e := j.HasIntent("create")
					if intent || e != nil || !os.IsNotExist(j.Load("create", &saved)) {
						t.Fatal("definitively rejected attempt retained intent/result", intent, e, saved)
					}
					// A fresh invocation after the throttle clears can submit again.
					if _, e = doEC2(context.Background(), j, "create", func() (string, error) { return "created", nil }, func() (string, bool, error) { t.Fatal("rejected attempt required discovery"); return "", false, nil }); e != nil {
						t.Fatal(e)
					}
				}
			})
		}
	}
}

func TestEC2ThrottleThenAmbiguousOutcomeRetainsIntent(t *testing.T) {
	for _, failure := range []error{errors.New("transport lost"), context.DeadlineExceeded, throttleError("ServiceUnavailable"), throttleError("Unavailable"), throttleError("InternalError")} {
		t.Run(failure.Error(), func(t *testing.T) {
			j := journal.Journal{Root: t.TempDir()}
			calls := 0
			_, err := journal.Do(context.Background(), j, "create", func() (string, error) {
				return retryEC2Mutation(context.Background(), func() (string, error) {
					calls++
					if calls == 1 {
						return "", throttleError("RequestLimitExceeded")
					}
					return "", failure
				}, func(context.Context, time.Duration) error { return nil })
			}, func() (string, bool, error) { return "", false, nil })
			intent, _ := j.HasIntent("create")
			if !errors.Is(err, failure) || calls != 2 || !intent {
				t.Fatal("ambiguous attempt lost", calls, intent, err)
			}
			_, err = doEC2(context.Background(), j, "create", func() (string, error) { t.Fatal("unknown create replayed"); return "", nil }, func() (string, bool, error) { return "", false, nil })
			if err == nil || !strings.Contains(err.Error(), "recovery_required") {
				t.Fatal(err)
			}
		})
	}
}

func TestEC2ThrottleBackoffCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			j := journal.Journal{Root: t.TempDir()}
			calls := 0
			start := time.Now()
			_, err := journal.Do(ctx, j, "create", func() (string, error) {
				return retryEC2Mutation(ctx, func() (string, error) { calls++; return "", throttleError("RequestLimitExceeded") }, func(ctx context.Context, _ time.Duration) error {
					if !deadline {
						cancel()
					}
					return waitRouteRetry(ctx, time.Hour)
				})
			}, func() (string, bool, error) { return "", false, nil })
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			}
			intent, _ := j.HasIntent("create")
			if !errors.Is(err, want) || calls != 1 || intent || time.Since(start) > time.Second {
				t.Fatal("backoff ignored cancellation or retained rejected intent", calls, intent, err)
			}
		})
	}
}

func ec2ThrottleResponse() *http.Response {
	return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader(`<Response><Errors><Error><Code>RequestLimitExceeded</Code><Message>rate exceeded</Message></Error></Errors></Response>`))}
}

// Run real network construction with every guarded create throttled twice. Key by
// the entire query so distinct segment/edge/management creates are all exercised.
func TestAWSNetworkGuardedCreatesRetryThrottle(t *testing.T) {
	for _, customCIDR := range []bool{false, true} {
		t.Run(fmt.Sprint(customCIDR), func(t *testing.T) {
			t.Parallel()
			f, api := newNetworkEC2(t)
			opts := api.Options()
			attempts := map[string]int{}
			opts.HTTPClient = optionHTTPClient(func(r *http.Request) (*http.Response, error) {
				if err := r.ParseForm(); err != nil {
					return nil, err
				}
				action := r.Form.Get("Action")
				switch action {
				case "CreateVpc", "CreateInternetGateway", "CreateSubnet", "CreateRouteTable", "CreateSecurityGroup", "CreateVpcEndpoint", "AssociateVpcCidrBlock":
					key := r.Form.Encode()
					attempts[key]++
					if attempts[key] <= 2 {
						return ec2ThrottleResponse(), nil
					}
				}
				return f.do(r)
			})
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := twoFirewallCommand()
			if customCIDR {
				c = twoNetworks()
				c.Source.Lab.Networks = map[string]lc.Network{"a": {CIDR: "172.31.26.0/24", Egress: "internet"}, "b": {CIDR: "172.20.240.0/24"}}
			}
			result, err := p.network(context.Background(), ec2.New(opts), c)
			if err != nil || !result.Resource.Complete {
				t.Fatal(result, err)
			}
			if (!customCIDR && len(attempts) < 15) || (customCIDR && len(f.cidrAssociations) != 1) {
				t.Fatal("missing network guarded creates", len(attempts))
			}
			for key, calls := range attempts {
				if calls != 3 {
					t.Fatal("request identity changed or retry missed", key, calls)
				}
				values, _ := url.ParseQuery(key)
				if values.Get("Action") == "CreateVpcEndpoint" && values.Get("ClientToken") != "owner-s3" {
					t.Fatal("endpoint lost token")
				}
			}
			// Only successful attempts reached the fixture, so one native object per create.
			if len(f.objects)+len(f.cidrAssociations) != len(attempts) {
				t.Fatal("duplicate or missing native result", len(f.objects), len(attempts))
			}
		})
	}
}

func TestAWSGuardedPersistentThrottleClearsOnlyRejectedIntent(t *testing.T) {
	t.Parallel()
	f, api := newNetworkEC2(t)
	opts := api.Options()
	calls := 0
	opts.HTTPClient = optionHTTPClient(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if r.Form.Get("Action") == "CreateInternetGateway" {
			calls++
			return ec2ThrottleResponse(), nil
		}
		return f.do(r)
	})
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := networkCommand(false)
	_, err := p.network(context.Background(), ec2.New(opts), c)
	if calls != 6 || err == nil || !strings.Contains(err.Error(), "after 6 attempts") {
		t.Fatal("incorrect cap", calls, err)
	}
	intent, e := p.Journal.HasIntent(c.Operation + "/gateway")
	var saved string
	if intent || e != nil || !os.IsNotExist(p.Journal.Load(c.Operation+"/gateway", &saved)) {
		t.Fatal("throttle retained rejected intent or result", intent, saved, e)
	}
	// Earlier successful children remain checkpointed and are reused on retry.
	if err := p.Journal.Load(c.Operation+"/vpc", &saved); err != nil || saved == "" {
		t.Fatal("throttle lost previous success", saved, err)
	}
	result, err := p.network(context.Background(), api, c)
	if err != nil || !result.Resource.Complete {
		t.Fatal("rejected create not retryable", result, err)
	}
	count := 0
	for _, object := range f.objects {
		if object.kind == "vpc" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("retry duplicated prior success", count)
	}
}

func TestAWSComputeGuardedCreatesRetryThrottle(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		t.Run(fmt.Sprint(gateway), func(t *testing.T) {
			t.Parallel()
			f, api, p, c := computeMatrixFixture(t, gateway)
			opts := api.Options()
			calls := 0
			query := ""
			opts.HTTPClient = optionHTTPClient(func(r *http.Request) (*http.Response, error) {
				if err := r.ParseForm(); err != nil {
					return nil, err
				}
				if r.Form.Get("Action") == "RunInstances" {
					calls++
					if query == "" {
						query = r.Form.Encode()
					}
					if query != r.Form.Encode() || r.Form.Get("ClientToken") == "" {
						t.Fatal("retry changed launch or token")
					}
					if calls <= 2 {
						return ec2ThrottleResponse(), nil
					}
				}
				return f.do(r)
			})
			result, err := computeMatrixRun(context.Background(), p, ec2.New(opts), c, gateway)
			if err != nil || !result.Resource.Complete || calls != 3 {
				t.Fatal(result, err, calls)
			}
			var id string
			if err = p.Journal.Load(c.Operation+"/instance", &id); err != nil || id != result.Resource.ID {
				t.Fatal("missing launch result", id, err)
			}
		})
	}
}

func TestAWSGuarded503AndTransportRemainUnknown(t *testing.T) {
	for _, code := range []string{"Unavailable", "ServiceUnavailable", "InternalError", "", "transport"} {
		t.Run(code, func(t *testing.T) {
			_, api := newNetworkEC2(t)
			opts := api.Options()
			calls := 0
			opts.HTTPClient = optionHTTPClient(func(r *http.Request) (*http.Response, error) {
				_ = r.ParseForm()
				if r.Form.Get("Action") != "CreateVpc" {
					return nil, fmt.Errorf("discovery unavailable")
				}
				calls++
				if code == "transport" {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`<Response><Errors><Error><Code>` + code + `</Code></Error></Errors></Response>`))}, nil
			})
			j := journal.Journal{Root: t.TempDir()}
			p := &Provider{Journal: j}
			for range 2 {
				_, err := p.network(context.Background(), ec2.New(opts), networkCommand(false))
				if err == nil {
					t.Fatal("ambiguous failure reported success")
				}
			}
			intent, err := j.HasIntent("owner/network/vpc")
			if calls != 1 || !intent || err != nil {
				t.Fatal("ambiguous create retried or lost intent", calls, intent, err)
			}
		})
	}
}

func TestAWSKeyImportsRetryThrottle(t *testing.T) {
	for _, appliance := range []bool{false, true} {
		t.Run(fmt.Sprint(appliance), func(t *testing.T) {
			var calls atomic.Int32
			query := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				w.Header().Set("Content-Type", "text/xml")
				switch r.Form.Get("Action") {
				case "ImportKeyPair":
					n := calls.Add(1)
					if query == "" {
						query = r.Form.Encode()
					}
					if query != r.Form.Encode() {
						t.Error("key import request changed")
					}
					if n <= 2 {
						w.WriteHeader(503)
						fmt.Fprint(w, `<Response><Errors><Error><Code>RequestLimitExceeded</Code></Error></Errors></Response>`)
						return
					}
					fmt.Fprint(w, `<ImportKeyPairResponse><keyPairId>key-owned</keyPairId></ImportKeyPairResponse>`)
				case "DescribeKeyPairs":
					fmt.Fprint(w, `<DescribeKeyPairsResponse><keySet><item><keyPairId>key-owned</keyPairId><keyName>labcentral-appliance-owner</keyName><publicKey>public-key</publicKey>`+fakeTags("appliance-key", "")+`</item></keySet></DescribeKeyPairsResponse>`)
				default:
					t.Error("unexpected action", r.Form.Get("Action"))
				}
			}))
			defer server.Close()
			t.Setenv("AWS_ACCESS_KEY_ID", "example")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "example")
			t.Setenv("AWS_ENDPOINT_URL_EC2", server.URL)
			c := networkCommand(false)
			c.Machine.PublicKey = "public-key"
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			key := c.Operation + "/build-key"
			var err error
			if appliance {
				api, _, e := client(context.Background(), c.Environment)
				if e != nil {
					t.Fatal(e)
				}
				_, err = p.importApplianceKey(context.Background(), api, c)
				key = c.Owner + "/appliance-key"
			} else {
				err = p.PrepareBuild(context.Background(), c, "public-key")
			}
			var id string
			if err != nil || calls.Load() != 3 || p.Journal.Load(key, &id) != nil || id != "key-owned" {
				t.Fatal("key retry/checkpoint failed", id, err, calls.Load())
			}
		})
	}
}

func TestAWSBuilderThrottleRetryPreservesProxyBody(t *testing.T) {
	for _, action := range []string{"RunInstances", "CreateImage"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			body := url.Values{"Action": {action}, "ClientToken": {"original-token"}, "Name": {"original-name"}}.Encode()
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if string(b) != body {
					t.Error("retry lost request body or token", string(b))
				}
				if calls.Add(1) <= 2 {
					w.WriteHeader(503)
					fmt.Fprint(w, `<Response><Errors><Error><Code>RequestLimitExceeded</Code></Error></Errors></Response>`)
					return
				}
				fmt.Fprint(w, "created")
			}))
			defer upstream.Close()
			g := &buildGuard{p: &Provider{Journal: journal.Journal{Root: t.TempDir()}}, c: lc.Command{Owner: "owner"}}
			target, _ := url.Parse(upstream.URL)
			endpoint, stop, err := nativeproxy.Start(context.Background(), nativeproxy.Config{Upstream: target, Protocol: "aws", Step: func(*http.Request, []byte) string { return action }, Do: g.do})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			for range 2 {
				r, e := http.Post(endpoint, "application/x-www-form-urlencoded", strings.NewReader(body))
				if e != nil {
					t.Fatal(e)
				}
				b, _ := io.ReadAll(r.Body)
				r.Body.Close()
				if r.StatusCode != 200 || string(b) != "created" {
					t.Fatal(r.StatusCode, string(b))
				}
			}
			// The second request replays the saved result, with no fourth submission.
			if calls.Load() != 3 {
				t.Fatal("builder did not checkpoint one successful result", calls.Load())
			}
			request, _, err := g.registerMutation([]byte(body))
			var result buildMutationResult
			if err != nil || g.p.Journal.Load(request.Key, &result) != nil || result.Reply.Status != 200 {
				t.Fatal("missing successful builder checkpoint", result, err)
			}
		})
	}
}
