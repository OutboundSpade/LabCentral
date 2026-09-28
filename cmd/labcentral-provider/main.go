package main

import (
	"context"
	"flag"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/access"
	"github.com/OutboundSpade/LabCentral/v2/build"
	"github.com/OutboundSpade/LabCentral/v2/internal/journal"
	awsplatform "github.com/OutboundSpade/LabCentral/v2/platform/aws"
	"github.com/OutboundSpade/LabCentral/v2/platform/proxmox"
	"github.com/OutboundSpade/LabCentral/v2/plugin"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

type router struct {
	aws, proxmox lc.Provider
	access       *access.Manager
}

func (r *router) Execute(ctx context.Context, c lc.Command, o lc.Observer) (lc.Result, error) {
	if c.Action == "access.open" || c.Action == "access.close" {
		return r.access.Execute(ctx, c)
	}
	switch c.Environment.Platform {
	case "aws":
		return r.aws.Execute(ctx, c, o)
	case "proxmox":
		return r.proxmox.Execute(ctx, c, o)
	}
	return lc.Result{}, fmt.Errorf("unsupported platform")
}
func main() {
	socket := flag.String("socket", "", "private Unix socket")
	state := flag.String("state", "", "provider-owned journal directory")
	flag.Parse()
	if *socket == "" || *state == "" {
		fmt.Fprintln(os.Stderr, "--socket and --state are required")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a := &awsplatform.Provider{Journal: journal.Journal{Root: filepath.Join(*state, "aws")}}
	p := &proxmox.Provider{Journal: journal.Journal{Root: filepath.Join(*state, "proxmox")}}
	ar := &build.Runner{Provider: a, Root: filepath.Join(*state, "builds"), Find: a.FindBuild, Prepare: a.PrepareBuild, Cleanup: a.CleanupBuild}
	ar.Validate = func(ctx context.Context, c lc.Command, r lc.Resource) (lc.Resource, error) {
		return build.ValidateNative(ctx, ar.Root, a, c, r)
	}
	pr := &build.Runner{Provider: p, Root: filepath.Join(*state, "builds"), Find: p.FindBuild, Cleanup: p.CleanupBuild}
	pr.Validate = func(ctx context.Context, c lc.Command, r lc.Resource) (lc.Resource, error) {
		return build.ValidateNative(ctx, pr.Root, p, c, r)
	}
	a.Build = ar.Run
	a.Abandon = ar.Abandon
	p.Build = pr.Run
	p.Abandon = pr.Abandon
	manager := access.NewManager()
	defer manager.Close()
	r := &router{a, p, manager}
	if err := plugin.Serve(ctx, *socket, r); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
