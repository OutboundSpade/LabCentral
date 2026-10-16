package aws

import (
	"context"
	"fmt"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type cleanupInventory struct {
	mu            sync.Mutex
	t             *testing.T
	exists        map[string]bool
	owners        map[string]string
	terminated    bool
	calls         []string
	target, fault string
	hit           bool
}

func (f *cleanupInventory) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	v := r.Form
	action := v.Get("Action")
	f.calls = append(f.calls, action)
	selected := action == f.target && !f.hit
	if selected {
		f.hit = true
	}
	code := ""
	status := 400
	if selected && f.fault != "response-loss" {
		code = map[string]string{"authorization": "UnauthorizedOperation", "validation": "InvalidParameterValue", "throttle": "RequestLimitExceeded", "server": "InternalError"}[f.fault]
		if f.fault == "server" {
			status = 500
		}
		if f.fault == "reset-before" {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
	}
	tag := func(id, role string) string {
		owner := f.owners[id]
		if owner == "" {
			owner = "owner"
		}
		return strings.Replace(fakeTags(role, ""), "<value>owner</value>", "<value>"+owner+"</value>", 1)
	}
	matches := func(id string) bool {
		owner := "owner"
		if x := f.owners[id]; x != "" {
			owner = x
		}
		for key, values := range v {
			if strings.HasPrefix(key, "Filter.") && strings.HasSuffix(key, ".Name") && values[0] == "tag:labcentral:owner" {
				if v.Get(strings.TrimSuffix(key, ".Name")+".Value.1") != owner {
					return false
				}
			}
		}
		return true
	}
	body := "<return>true</return>"
	if code == "" {
		switch action {
		case "RunInstances":
			f.exists["i-test"] = true
			f.terminated = false
			body = "<instancesSet><item><instanceId>i-test</instanceId>" + tag("i-test", "server") + "</item></instancesSet>"
		case "CreateImage":
			f.exists["ami-test"] = true
			body = "<imageId>ami-test</imageId>"
		case "DescribeInstances":
			body = "<reservationSet>"
			if f.exists["i-test"] && matches("i-test") {
				state, number := "running", 16
				if f.terminated {
					state, number = "terminated", 48
				}
				body += fmt.Sprintf("<item><instancesSet><item><instanceId>i-test</instanceId><instanceState><name>%s</name><code>%d</code></instanceState>%s</item></instancesSet></item>", state, number, tag("i-test", "server"))
			}
			body += "</reservationSet>"
			if !f.exists["i-test"] && v.Get("InstanceId.1") != "" {
				code = "InvalidInstanceID.NotFound"
			}
		case "TerminateInstances":
			if f.owners["i-test"] != "" && f.owners["i-test"] != "owner" {
				f.t.Error("foreign instance terminated")
			}
			f.terminated = true
		case "DescribeNetworkInterfaces":
			body = "<networkInterfaceSet/>"
		case "DescribeVolumes":
			body = "<volumeSet>"
			if f.exists["vol-test"] && matches("vol-test") {
				body += "<item><volumeId>vol-test</volumeId><status>available</status>" + tag("vol-test", "server") + "</item>"
			}
			body += "</volumeSet>"
			if !f.exists["vol-test"] && v.Get("VolumeId.1") != "" {
				code = "InvalidVolume.NotFound"
			}
		case "DeleteVolume":
			if f.owners["vol-test"] != "" && f.owners["vol-test"] != "owner" {
				f.t.Error("foreign volume deleted")
			}
			f.exists["vol-test"] = false
		case "DescribeImages":
			body = "<imagesSet>"
			if f.exists["ami-test"] {
				body += "<item><imageId>ami-test</imageId><state>available</state>" + tag("ami-test", "image") + "</item>"
			}
			body += "</imagesSet>"
		case "DeregisterImage":
			if f.owners["ami-test"] != "" && f.owners["ami-test"] != "owner" {
				f.t.Error("foreign image removed")
			}
			f.exists["ami-test"] = false
		case "DescribeSnapshots":
			body = "<snapshotSet>"
			if f.exists["snap-test"] && matches("snap-test") {
				body += "<item><snapshotId>snap-test</snapshotId><status>completed</status>" + tag("snap-test", "image") + "</item>"
			}
			body += "</snapshotSet>"
			if !f.exists["snap-test"] && v.Get("SnapshotId.1") != "" {
				code = "InvalidSnapshot.NotFound"
			}
		case "DeleteSnapshot":
			if f.owners["snap-test"] != "" && f.owners["snap-test"] != "owner" {
				f.t.Error("foreign snapshot deleted")
			}
			f.exists["snap-test"] = false
		case "DescribeKeyPairs":
			body = "<keySet>"
			if f.exists["key-test"] {
				body += "<item><keyPairId>key-test</keyPairId><keyName>labcentral-owner</keyName>" + tag("key-test", "build-key") + "</item>"
			}
			body += "</keySet>"
			if !f.exists["key-test"] {
				code = "InvalidKeyPair.NotFound"
			}
		case "DeleteKeyPair":
			if f.owners["key-test"] != "" && f.owners["key-test"] != "owner" {
				f.t.Error("foreign key deleted")
			}
			f.exists["key-test"] = false
		default:
			f.t.Errorf("unexpected cleanup action %s", action)
		}
	}
	if selected && f.fault == "response-loss" {
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	if code != "" {
		w.WriteHeader(status)
		fmt.Fprintf(w, "<Response><Errors><Error><Code>%s</Code><Message>injected</Message></Error></Errors></Response>", code)
		return
	}
	fmt.Fprintf(w, "<%sResponse>%s</%sResponse>", action, body, action)
}
func cleanupMatrixFixture(t *testing.T) (*cleanupInventory, *ec2.Client) {
	t.Helper()
	f := &cleanupInventory{t: t, exists: map[string]bool{"i-test": true, "vol-test": true, "ami-test": true, "snap-test": true, "key-test": true}, owners: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	return f, ec2.New(ec2.Options{Region: "us-east-1", BaseEndpoint: aw.String(server.URL), Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), RetryMaxAttempts: 1})
}
func cleanupMatrixCommand(kind string) lc.Command {
	c := lc.Command{Owner: "owner", Prior: lc.Resource{Kind: kind, Name: "server", Owner: "owner", ID: "i-test", Children: []lc.NativeResource{{Kind: "volume", ID: "vol-test"}}}}
	if kind == "image" {
		c.Prior = lc.Resource{Kind: "image", Name: "image", Owner: "owner", ID: "ami-test", Children: []lc.NativeResource{{Kind: "snapshot", ID: "snap-test"}}}
	}
	return c
}
func TestAWSCleanupFailureMatrix(t *testing.T) {
	for _, step := range []string{"TerminateInstances", "DeleteVolume", "DeregisterImage", "DeleteSnapshot", "DeleteKeyPair"} {
		for _, fault := range []string{"response-loss", "reset-before", "authorization", "validation", "throttle", "server"} {
			t.Run(step+"/"+fault, func(t *testing.T) {
				f, a := cleanupMatrixFixture(t)
				f.target, f.fault = step, fault
				p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
				c := cleanupMatrixCommand("machine")
				if step == "DeregisterImage" || step == "DeleteSnapshot" {
					c = cleanupMatrixCommand("image")
				}
				run := func() error {
					if step == "DeleteKeyPair" {
						return p.cleanupBuild(context.Background(), a, lc.Command{Owner: "owner", Image: lc.PlannedImage{Name: "server"}})
					}
					result, err := p.destroy(context.Background(), a, c)
					if err == nil && !result.Absent {
						return fmt.Errorf("deletion not confirmed")
					}
					return err
				}
				if err := run(); err == nil {
					t.Fatal("unknown native cleanup reported done")
				}
				f.mu.Lock()
				hit := f.hit
				f.mu.Unlock()
				if !hit {
					t.Fatal("selected mutation not reached")
				}
				if err := run(); err != nil {
					t.Fatal("recovery failed", err)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if c.Prior.Kind == "image" {
					if f.exists["ami-test"] || f.exists["snap-test"] {
						t.Fatal("image/snapshot leak")
					}
				} else if !f.terminated || f.exists["vol-test"] {
					t.Fatal("instance/volume leak")
				}
				if step == "DeleteKeyPair" && f.exists["key-test"] {
					t.Fatal("keypair leak")
				}
			})
		}
	}
}
func TestAWSMachineImageOwnershipAndAbsenceMatrix(t *testing.T) {
	for _, id := range []string{"i-test", "vol-test", "ami-test", "snap-test", "key-test"} {
		for _, state := range []string{"absent", "foreign"} {
			t.Run(id+"/"+state, func(t *testing.T) {
				f, a := cleanupMatrixFixture(t)
				if state == "absent" {
					f.exists[id] = false
				} else {
					f.owners[id] = "foreign"
				}
				c := cleanupMatrixCommand("machine")
				if id == "ami-test" || id == "snap-test" {
					c = cleanupMatrixCommand("image")
				}
				run := func() error {
					p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
					if id == "key-test" {
						return p.cleanupBuild(context.Background(), a, lc.Command{Owner: "owner", Image: lc.PlannedImage{Name: "server"}})
					}
					_, err := p.destroy(context.Background(), a, c)
					return err
				}
				err := run()
				if state == "foreign" && id == "vol-test" && f.terminated {
					t.Fatal("termination deleted an unverified child volume")
				}
				if state == "foreign" {
					if err == nil || !strings.Contains(err.Error(), "ownership") {
						t.Fatal("ownership loss hidden", err)
					}
					f.mu.Lock()
					exists := f.exists[id]
					f.owners[id] = "owner"
					f.mu.Unlock()
					if !exists {
						t.Fatal("foreign resource deleted")
					}
					err = run()
				}
				if err != nil {
					t.Fatal("confirmed absence not reconciled", err)
				}
			})
		}
	}
}
