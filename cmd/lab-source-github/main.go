package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	wire "github.com/OutboundSpade/LabCentral/api/plugin/v1"
	runtime "github.com/OutboundSpade/LabCentral/pkg/plugin-sdk-go/runtime"
	source "github.com/OutboundSpade/LabCentral/sources/github"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	root := flag.String("state", "", "private plugin-owned journal directory")
	flag.Parse()
	s, err := source.New(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	executable, err := os.Executable()
	if err != nil {
		panic(err)
	}
	file, err := os.Open(executable)
	if err != nil {
		panic(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		panic(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = runtime.Serve(ctx, runtime.Services{Info: &wire.PluginInfo{PluginId: source.PluginID, PluginVersion: "0.1.0", BuildDigest: fmt.Sprintf("sha256:%x", hash.Sum(nil)), Categories: []string{"source"}, SourceKinds: []string{"github"}, SupportedSourceProtocols: []*wire.ProtocolVersionRange{{Major: 1}}, Features: []string{"artifact-transfer-v1", "source-collection-v1"}}, Runtime: s, Source: s, Artifacts: s})
	if err != nil {
		fmt.Fprintln(os.Stderr, "GitHub source service stopped")
		os.Exit(1)
	}
}
