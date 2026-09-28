package aws

import (
	"context"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSnapshotWithRemovedOwnershipIsReported(t *testing.T) {
	deletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "text/xml")
		switch r.Form.Get("Action") {
		case "DescribeSnapshots":
			if r.Form.Get("SnapshotId.1") != "" {
				fmt.Fprint(w, `<DescribeSnapshotsResponse><snapshotSet><item><snapshotId>snap-recorded</snapshotId><status>completed</status></item></snapshotSet></DescribeSnapshotsResponse>`)
			} else {
				fmt.Fprint(w, `<DescribeSnapshotsResponse><snapshotSet/></DescribeSnapshotsResponse>`)
			}
		case "DeleteSnapshot":
			deletes++
			fmt.Fprint(w, `<DeleteSnapshotResponse><return>true</return></DeleteSnapshotResponse>`)
		default:
			t.Errorf("unexpected call %s", r.Form.Get("Action"))
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	a := ec2.New(ec2.Options{Region: "us-east-1", BaseEndpoint: aw.String(server.URL), Credentials: aw.CredentialsProviderFunc(func(context.Context) (aw.Credentials, error) {
		return aw.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	})})
	err := cleanupSnapshots(context.Background(), a, lc.Command{Owner: "build-test", Prior: lc.Resource{Children: []lc.NativeResource{{Kind: "snapshot", ID: "snap-recorded"}}}})
	if err == nil || deletes != 0 {
		t.Fatalf("stale ownership was ignored: deletes=%d err=%v", deletes, err)
	}
}
