package aws

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lc "github.com/OutboundSpade/LabCentral/v2"
	aw "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	t "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestLegacyNICOwnershipRequiresOriginalPrimaryOrReceipt(tst *testing.T) {
	for _, tc := range []struct {
		name                  string
		index                 int
		recorded, foreign, ok bool
	}{{"original-primary", 0, false, false, true}, {"unrecorded-secondary", 1, false, false, false}, {"recorded-secondary", 1, true, false, true}, {"foreign-primary", 0, false, true, false}, {"foreign-recorded-secondary", 1, true, true, false}} {
		tst.Run(tc.name, func(tst *testing.T) {
			tags := ""
			if tc.foreign {
				tags = strings.Replace(fakeTags("machine", ""), "<value>owner</value>", "<value>foreign</value>", 1)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `<DescribeNetworkInterfacesResponse><networkInterfaceSet><item><networkInterfaceId>eni-example</networkInterfaceId><attachment><instanceId>i-example</instanceId><deviceIndex>%d</deviceIndex></attachment>%s</item></networkInterfaceSet></DescribeNetworkInterfacesResponse>`, tc.index, tags)
			}))
			defer server.Close()
			api := ec2.New(ec2.Options{Region: "us-east-1", BaseEndpoint: aw.String(server.URL), Credentials: credentials.NewStaticCredentialsProvider("example", "example", ""), RetryMaxAttempts: 1})
			c := lc.Command{Owner: "owner"}
			if tc.recorded {
				c.Prior.Children = []lc.NativeResource{{Kind: "eni", ID: "eni-example"}}
			}
			i := t.Instance{InstanceId: aw.String("i-example"), NetworkInterfaces: []t.InstanceNetworkInterface{{NetworkInterfaceId: aw.String("eni-example")}}}
			err := verifyInstanceChildren(context.Background(), api, c, i, false)
			if (err == nil) != tc.ok {
				tst.Fatal(tc.name, err)
			}
		})
	}
}
