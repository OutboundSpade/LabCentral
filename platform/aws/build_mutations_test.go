package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/nativeproxy"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func TestAWSBuilderCreateJournalMatrix(t *testing.T) {
	for _, action := range []string{"RunInstances", "CreateImage"} {
		for _, fault := range []string{"success", "preflight", "response-loss", "empty-unknown", "foreign", "authorization", "validation", "throttle", "server"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				ctx := context.Background()
				inventory, api := cleanupMatrixFixture(t)
				root := t.TempDir()
				c := lc.Command{Owner: "owner", Image: lc.PlannedImage{Name: "image"}}
				g := &buildGuard{p: &Provider{Journal: journal.Journal{Root: root}}, api: api, c: c}
				values := url.Values{"Action": {action}, "ClientToken": {"example-token"}, "Name": {"labcentral-owner"}}
				body := []byte(values.Encode())
				calls := 0
				id := "i-test"
				if action == "CreateImage" {
					id = "ami-test"
				}
				if fault == "empty-unknown" {
					inventory.exists[id] = false
				}
				if fault == "foreign" {
					inventory.owners[id] = "foreign"
				}
				native := func(context.Context) (nativeproxy.Reply, error) {
					calls++
					switch fault {
					case "preflight":
						return nativeproxy.Reply{}, nativeproxy.BeforeSubmission(errors.New("ownership_mismatch: guard"))
					case "response-loss", "empty-unknown", "foreign":
						return nativeproxy.Reply{}, errors.New("lost after send")
					case "authorization", "validation", "throttle", "server":
						code := map[string]string{"authorization": "UnauthorizedOperation", "validation": "InvalidParameterValue", "throttle": "RequestLimitExceeded", "server": "InternalError"}[fault]
						return nativeproxy.Reply{Status: 400, Body: []byte(fmt.Sprintf("<Response><Errors><Error><Code>%s</Code></Error></Errors></Response>", code))}, nil
					}
					return nativeproxy.Reply{Status: 200, Body: []byte("native reply")}, nil
				}
				_, _ = g.do(ctx, &http.Request{}, body, native)
				g = &buildGuard{p: &Provider{Journal: journal.Journal{Root: root}}, api: api, c: c}
				res, err := g.do(ctx, &http.Request{}, body, native)
				if fault == "preflight" {
					if calls != 2 || err == nil || !nativeproxy.IsBeforeSubmission(err) {
						t.Fatal("local guard denial lost definite outcome", calls, err)
					}
					if e := g.p.reconcileBuildMutations(ctx, api, c); e != nil {
						t.Fatal(e)
					}
					return
				}
				rejected := fault == "authorization" || fault == "validation" || fault == "throttle"
				if rejected {
					if calls != 2 || err != nil || res.Status != 400 {
						t.Fatal(calls, res, err)
					}
					return
				}
				if calls != 1 {
					t.Fatal("uncertain native create replayed", calls)
				}
				if fault == "empty-unknown" || fault == "foreign" {
					if err == nil {
						t.Fatal("uncertain/foreign outcome discarded")
					}
					if e := g.p.reconcileBuildMutations(ctx, api, c); e == nil || !strings.Contains(e.Error(), "recovery_required") {
						t.Fatal("cleanup lost actionable intent", e)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if fault == "success" {
					if res.Status != 200 {
						t.Fatal(res)
					}
				} else if res.Status != 409 {
					t.Fatal("recovery invented native reply", res)
				}
				if err = g.p.reconcileBuildMutations(ctx, api, c); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestAWSBuilderGuardRefusesForeignNativeCleanup(t *testing.T) {
	f, api := cleanupMatrixFixture(t)
	g := &buildGuard{p: &Provider{Journal: journal.Journal{Root: t.TempDir()}}, api: api, c: lc.Command{Owner: "owner"}}
	for _, item := range []struct{ action, key, id string }{{"TerminateInstances", "InstanceId.1", "i-test"}, {"DeleteVolume", "VolumeId", "vol-test"}, {"DeleteSnapshot", "SnapshotId", "snap-test"}, {"DeregisterImage", "ImageId", "ami-test"}} {
		f.owners[item.id] = "foreign"
		_, err := g.before(context.Background(), nil, []byte(url.Values{"Action": {item.action}, item.key: {item.id}}.Encode()))
		if err == nil || !strings.Contains(err.Error(), "ownership_mismatch") {
			t.Fatal(item.action, err)
		}
		f.owners[item.id] = "owner"
	}
}

func TestAWSBuilderSDKUsesCapabilityEndpointRoot(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/" {
			t.Errorf("upstream capability leaked: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, "<DescribeRegionsResponse><regionInfo/></DescribeRegionsResponse>")
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	endpoint, stop, err := nativeproxy.Start(context.Background(), nativeproxy.Config{Upstream: target, Protocol: "aws", Step: func(*http.Request, []byte) string { return "packer.DescribeRegions" }})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	sdk := ec2.New(ec2.Options{Region: "us-east-1", BaseEndpoint: aw.String(endpoint + "/"), Credentials: credentials.NewStaticCredentialsProvider("example", "example", ""), RetryMaxAttempts: 1})
	if _, err = sdk.DescribeRegions(context.Background(), &ec2.DescribeRegionsInput{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestAWSBuilderRecordsNativeXML(t *testing.T) {
	g := &buildGuard{p: &Provider{Journal: journal.Journal{Root: t.TempDir()}}, c: lc.Command{Owner: "owner", Image: lc.PlannedImage{Name: "image"}}}
	for _, item := range []struct{ action, xml string }{{"DescribeImages", `<DescribeImagesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><imagesSet/></DescribeImagesResponse>`}, {"RunInstances", `<RunInstancesResponse><instancesSet><item><instanceId>i-example</instanceId></item></instancesSet></RunInstancesResponse>`}} {
		if err := g.after(context.Background(), nil, []byte("Action="+item.action), nativeproxy.Reply{Status: 200, Body: []byte(item.xml)}); err != nil {
			t.Fatal(item.action, err)
		}
	}
}
