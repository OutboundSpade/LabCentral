package aws

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	"github.com/OutboundSpade/LabCentral/v2/internal/remote"
	"github.com/OutboundSpade/LabCentral/v2/internal/sshtest"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"golang.org/x/crypto/ssh"
)

func TestApplianceKeyLostImportAndOwnedCleanup(t *testing.T) {
	for _, altered := range []string{"", "foreign-role", "replaced"} {
		t.Run(altered, func(t *testing.T) {
			_, pub := sshtest.Key(t)
			present := false
			imports, deletes := 0, 0
			role, id := "appliance-key", "key-original"
			api := ec2.New(ec2.Options{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("fixture", "fixture", ""), RetryMaxAttempts: 1, HTTPClient: optionHTTPClient(func(r *http.Request) (*http.Response, error) {
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				action := r.Form.Get("Action")
				body := ""
				switch action {
				case "ImportKeyPair":
					imports++
					present = true
					return nil, fmt.Errorf("fixture response lost after import")
				case "DescribeKeyPairs":
					if present {
						body = "<keySet><item><keyPairId>" + id + "</keyPairId><keyName>labcentral-appliance-owner</keyName><publicKey>" + pub + "</publicKey>" + fakeTags(role, "") + "</item></keySet>"
					} else {
						body = "<keySet/>"
					}
				case "DeleteKeyPair":
					deletes++
					if r.Form.Get("KeyPairId") != "key-original" {
						t.Error("delete used unrecorded identity")
					}
					present = false
				default:
					t.Error("unexpected key operation", action)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/xml"}}, Body: io.NopCloser(strings.NewReader("<" + action + "Response>" + body + "</" + action + "Response>"))}, nil
			})})
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := lc.Command{Owner: "owner", Machine: lc.PlannedMachine{PublicKey: pub}}
			if _, err := p.importApplianceKey(context.Background(), api, c); err == nil {
				t.Fatal("lost import response accepted")
			}
			if _, err := p.importApplianceKey(context.Background(), api, c); err != nil {
				t.Fatal("exact key did not recover", err)
			}
			if altered == "foreign-role" {
				role = "other"
			}
			if altered == "replaced" {
				id = "key-replacement"
			}
			if altered != "" {
				if _, err := p.importApplianceKey(context.Background(), api, c); err == nil {
					t.Fatal("changed key admitted before launch")
				}
			}
			err := p.cleanupApplianceKey(context.Background(), api, c)
			if (err == nil) != (altered == "") {
				t.Fatal("incorrect cleanup ownership result", err)
			}
			if imports != 1 || (altered == "" && (deletes != 1 || present)) || (altered != "" && deletes != 0) {
				t.Fatal("key replayed or foreign key removed", imports, deletes, present)
			}
		})
	}
}

func TestApplianceConsoleIdentityIsBoundedAndOwnerScoped(t *testing.T) {
	_, pub := sshtest.Key(t)
	key, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pub))
	want := ssh.FingerprintSHA256(key)
	for _, tc := range []struct{ kind, owner, output, want string }{
		{"vyos", "owner", "LABCENTRAL_HOST_KEY other " + pub, ""},
		{"vyos", "owner", "LABCENTRAL_HOST_KEY owner " + pub, want},
		{"vyos", "owner", "LABCENTRAL_HOST_KEY owner invalid-key", ""},
		{"panos", "owner", "certificate aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99", ""},
		{"panos", "owner", "SSH Public key fingerprints\nrsa  aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99\n", "MD5:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"},
	} {
		got, err := consoleHostPins(tc.kind, tc.owner, tc.output)
		if err != nil || got != tc.want {
			t.Fatalf("%s identity=%q want=%q err=%v", tc.kind, got, tc.want, err)
		}
	}
	if _, err := consoleHostPins("vyos", "owner", strings.Repeat("x", (1<<20)+1)); err == nil {
		t.Fatal("unbounded console output")
	}
}

func applianceCLIFixture(t *testing.T, respond func(string) string) *ssh.Client {
	t.Helper()
	s := sshtest.NewShell(t, func(ch ssh.Channel) {
		io.WriteString(ch, "admin@firewall> ")
		scanner := bufio.NewScanner(ch)
		for scanner.Scan() {
			out := respond(scanner.Text())
			if out == "DROP" {
				return
			}
			io.WriteString(ch, out+"\r\nadmin@firewall> ")
		}
	})
	private, _ := sshtest.Key(t)
	client, err := remote.Dial(context.Background(), s.Address, "admin", private, s.HostKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestPANOSCommitReceiptSurvivesLostResponseWithoutRecommit(t *testing.T) {
	for _, committed := range []bool{true, false} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			var mu sync.Mutex
			marker := ""
			commits := 0
			lost := false
			client := applianceCLIFixture(t, func(command string) string {
				mu.Lock()
				defer mu.Unlock()
				if strings.HasPrefix(command, "set shared tag labcentral-") {
					marker = strings.Fields(command)[3]
				}
				if command == "commit" {
					commits++
					if !lost {
						lost = true
						return "DROP"
					}
				}
				if strings.HasPrefix(command, "show config running | match ") && committed && marker != "" {
					return `<entry name="` + marker + `">`
				}
				return command
			})
			p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
			c := lc.Command{Owner: "owner", Operation: "owner/palo", Machine: lc.PlannedMachine{Name: "palo"}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.configurePANOS(ctx, client, c, []string{"set zone inside network layer3 ethernet1/2"}); err == nil {
				t.Fatal("lost commit response was accepted")
			}
			err := p.configurePANOS(ctx, client, c, []string{"set zone inside network layer3 ethernet1/2"})
			if (err == nil) != committed {
				t.Fatal("incorrect commit recovery", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if commits != 1 {
				t.Fatal("uncertain commit was replayed", commits)
			}
		})
	}
}

func TestApplianceCLIRejectsErrorsAndStopsOnCancellation(t *testing.T) {
	client := applianceCLIFixture(t, func(command string) string { return "Invalid syntax: " + command })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := applianceCLI(ctx, client, []string{"configure"}); err == nil {
		t.Fatal("CLI syntax error accepted")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := applianceCLI(ctx, client, nil); err == nil {
		t.Fatal("cancelled CLI completed")
	}
}

func TestPANOSRecoveryRetainsOriginalRejectionWithoutReplay(t *testing.T) {
	var mu sync.Mutex
	configures := 0
	client := applianceCLIFixture(t, func(command string) string {
		mu.Lock()
		defer mu.Unlock()
		if command == "configure" {
			configures++
			return "Invalid syntax"
		}
		return command
	})
	p := &Provider{Journal: journal.Journal{Root: t.TempDir()}}
	c := lc.Command{Owner: "owner", Operation: "owner/palo", Machine: lc.PlannedMachine{Name: "palo"}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for range 2 {
		err := p.configurePANOS(ctx, client, c, nil)
		if err == nil || !strings.Contains(err.Error(), "appliance rejected configuration step 3") {
			t.Fatal("original rejection lost", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if configures != 1 {
		t.Fatal("uncertain candidate configuration replayed", configures)
	}
}
