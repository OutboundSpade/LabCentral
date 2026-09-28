package plugin

import (
	"context"
	"encoding/json"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/wire"
	"github.com/OutboundSpade/LabCentral/v2/internal/wireconvert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestTypedProtocolRoundTrip(t *testing.T) {
	c := lc.Command{ClientPrivateKey: []byte("transient"), Operation: "op", Environment: lc.Environment{Version: 2, Platform: "aws", Bases: map[string]lc.Base{"ubuntu": {ID: "ami", DiskGiB: 8}}}, Source: lc.Source{Files: []lc.File{{Path: "lab.yaml", Content: []byte("version: 2")}}}, Prior: lc.Resource{Data: json.RawMessage(`{"native":"state"}`)}}
	w := &wire.Command{}
	if e := wireconvert.Encode(c, w); e != nil {
		t.Fatal(e)
	}
	var got lc.Command
	if e := wireconvert.Decode(w, &got); e != nil {
		t.Fatal(e)
	}
	if got.Environment.Bases["ubuntu"].ID != "ami" || string(got.Prior.Data) != string(c.Prior.Data) || string(got.ClientPrivateKey) != "transient" {
		t.Fatalf("lossy protocol %+v", got)
	}
}

type fake struct{}

func (fake) Execute(ctx context.Context, c lc.Command, o lc.Observer) (lc.Result, error) {
	o(lc.Event{Version: 2, Stage: "test"})
	return lc.Result{Message: "complete"}, nil
}
func TestSocketProtocolRejectsWrongVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := filepath.Join(t.TempDir(), "rpc")
	go Serve(ctx, sock, fake{})
	conn, e := grpc.NewClient("passthrough:///"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	deadline, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	stream, e := wire.NewPlatformClient(conn).Execute(deadline, &wire.Request{Version: 1}, grpc.WaitForReady(true))
	if e == nil {
		_, e = stream.Recv()
	}
	if e == nil {
		t.Fatal("wrong version accepted")
	}
}
