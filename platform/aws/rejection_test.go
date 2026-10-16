package aws

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/build"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/nativeproxy"
	"github.com/aws/smithy-go"
)

func TestBuilderQuotaDenialCanAbandonAndRebuild(t *testing.T) {
	for _, code := range []string{"InstanceLimitExceeded", "VcpuLimitExceeded", "InsufficientFreeAddressesInSubnet", "InsufficientInstanceCapacity", "NetworkInterfaceLimitExceeded", "VolumeLimitExceeded"} {
		t.Run(code, func(t *testing.T) {
			ctx := context.Background()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				action := r.Form.Get("Action")
				w.Header().Set("Content-Type", "text/xml")
				switch action {
				case "DescribeInstances":
					fmt.Fprint(w, `<DescribeInstancesResponse><reservationSet/></DescribeInstancesResponse>`)
				case "DescribeImages":
					fmt.Fprint(w, `<DescribeImagesResponse><imagesSet/></DescribeImagesResponse>`)
				case "DescribeVolumes":
					fmt.Fprint(w, `<DescribeVolumesResponse><volumeSet/></DescribeVolumesResponse>`)
				case "DescribeNetworkInterfaces":
					fmt.Fprint(w, `<DescribeNetworkInterfacesResponse><networkInterfaceSet/></DescribeNetworkInterfacesResponse>`)
				default:
					t.Errorf("unexpected mutation during denied-build abandonment: %s", action)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			t.Setenv("AWS_ENDPOINT_URL_EC2", server.URL)
			t.Setenv("AWS_ACCESS_KEY_ID", "example")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "example")
			env := lc.Environment{AWS: lc.AWSConfig{Region: "us-east-1"}}
			api, _, err := client(ctx, env)
			if err != nil {
				t.Fatal(err)
			}
			c := lc.Command{Owner: "example-build", Action: "image.abandon", Environment: env, Image: lc.PlannedImage{Name: "desktop", Base: lc.Base{OS: "windows"}}}
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			g := &buildGuard{p: p, api: api, c: c}
			body := []byte(url.Values{"Action": {"RunInstances"}, "ClientToken": {"example-token"}}.Encode())
			reply, err := g.do(ctx, &http.Request{}, body, func(context.Context) (nativeproxy.Reply, error) {
				return nativeproxy.Reply{Status: 400, Body: []byte(fmt.Sprintf(`<Response><Errors><Error><Code>%s</Code></Error></Errors></Response>`, code))}, nil
			})
			if err != nil || reply.Status != 400 {
				t.Fatal(reply, err)
			}
			runner := &build.Runner{Root: t.TempDir(), Provider: p, Find: p.FindBuild, Cleanup: p.CleanupBuild}
			result, err := runner.Abandon(ctx, c, nil)
			if err != nil || !result.Absent {
				t.Fatal("definite quota denial stranded abandonment", result, err)
			}
			// A restart may submit again after correcting capacity, unlike an unknown create.
			g = &buildGuard{p: &Provider{Journal: p.Journal}, api: api, c: c}
			calls := 0
			reply, err = g.do(ctx, &http.Request{}, body, func(context.Context) (nativeproxy.Reply, error) {
				calls++
				return nativeproxy.Reply{Status: 200, Body: []byte("created")}, nil
			})
			if err != nil || reply.Status != 200 || calls != 1 {
				t.Fatal("rebuild blocked after definite denial", reply, err, calls)
			}
			if !rejectedEC2Error(&smithy.OperationError{ServiceID: "EC2", OperationName: "RunInstances", Err: &smithy.GenericAPIError{Code: code}}) {
				t.Fatal("SDK and Packer classifiers diverged")
			}
			if definiteEC2Rejection("CreateImage", code) {
				t.Fatal("launch rejection applied to another action")
			}
		})
	}
}
