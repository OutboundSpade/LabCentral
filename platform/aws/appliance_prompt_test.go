package aws

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"golang.org/x/crypto/ssh"
)

func TestApplianceVendorPromptsAndOutputBoundaries(t *testing.T) {
	for _, prompt := range []string{"vyos@edge:~$ ", "vyos@edge:/config$ ", "admin@firewall> ", "admin@firewall(active)# "} {
		t.Run(prompt, func(t *testing.T) {
			server := sshtest.NewShell(t, func(ch ssh.Channel) {
				io.WriteString(ch, "\x1b]0;vendor title\x07\x1b=\x1b[?2004h\r\x1b[2K\x1b[32m"+prompt+"\x1b[0m")
				scanner := bufio.NewScanner(ch)
				for scanner.Scan() {
					io.WriteString(ch, "\r\nprogress#")
					time.Sleep(20 * time.Millisecond)
					io.WriteString(ch, "\r\nother@machine> ")
					time.Sleep(20 * time.Millisecond)
					io.WriteString(ch, "\r\nfinished "+scanner.Text()+"\r\x1b[K\x1b[?1l\x1b>\x1b[?2004h"+prompt)
				}
			})
			key, _ := sshtest.Key(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			client, err := remote.Dial(ctx, server.Address, "vyos", key, server.HostKey)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			out, err := applianceCLI(ctx, client, []string{"show version", "show interfaces"})
			if err != nil || !strings.Contains(out, "finished show version") || !strings.Contains(out, "finished show interfaces") {
				t.Fatalf("premature prompt or failed CLI: %q %v", out, err)
			}
		})
	}
}

func TestWaitVyOSStandardPromptAndPinnedIdentity(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			server := sshtest.NewShell(t, func(ch ssh.Channel) {
				io.WriteString(ch, "\x1b[?2004hvyos@edge:~$ ")
				scanner := bufio.NewScanner(ch)
				for scanner.Scan() {
					if scanner.Text() != "show version" {
						t.Error("unexpected command", scanner.Text())
					}
					output := "not the vendor"
					if ready {
						output = "Version: VyOS 1.4.0"
					}
					io.WriteString(ch, "\r\n"+output+"\r\x1b[K\x1b[?1l\x1b>\x1b[?2004hvyos@edge:~$ ")
				}
			})
			key, _ := sshtest.Key(t)
			api := ec2.New(ec2.Options{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("fixture", "fixture", ""), HTTPClient: optionHTTPClient(func(r *http.Request) (*http.Response, error) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("Action") != "GetConsoleOutput" || r.Form.Get("InstanceId") != "i-fixture" {
					t.Error("wrong console target", r.Form)
				}
				console := base64.StdEncoding.EncodeToString([]byte("LABCENTRAL_HOST_KEY owner " + server.HostKey))
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("<GetConsoleOutputResponse><instanceId>i-fixture</instanceId><output>" + console + "</output></GetConsoleOutputResponse>"))}, nil
			})})
			c := lc.Command{Owner: "owner", ClientPrivateKey: key, Source: lc.Source{Lab: lc.Lab{Images: map[string]lc.Image{"vyos": {Base: "vyos", Appliance: &lc.ApplianceImage{Kind: "vyos"}}}}}, Environment: lc.Environment{Platform: "aws", Bases: map[string]lc.Base{"vyos": {ID: "ami-vyos", Appliance: "vyos", User: "vyos"}}}, Machine: lc.PlannedMachine{Name: "edge", Appliance: "vyos", NativeImage: "ami-vyos", Spec: lc.Machine{Image: "vyos"}}}
			dialer := &remote.Dialer{}
			defer dialer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			result, err := (&Provider{}).waitAppliance(ctx, api, dialer, c, lc.Result{Resource: lc.Resource{ID: "i-fixture", Address: server.Address}})
			if ready && (err != nil || !result.Resource.Complete || !strings.HasPrefix(result.Resource.HostPublicKey, "SHA256:")) {
				t.Fatal("VyOS readiness failed", result, err)
			}
			if !ready && err == nil {
				t.Fatal("non-VyOS shell accepted")
			}
		})
	}
}
