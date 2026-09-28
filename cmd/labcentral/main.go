package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/internal/cli"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		jsonOutput := false
		for _, arg := range os.Args[1:] {
			if arg == "--json" || arg == "--json=true" {
				jsonOutput = true
			}
		}
		if jsonOutput {
			var typed *lc.Error
			if !errors.As(err, &typed) {
				typed = &lc.Error{Code: lc.RecoveryRequired, Message: err.Error()}
			}
			_ = json.NewEncoder(os.Stderr).Encode(struct {
				Version int       `json:"version"`
				Error   *lc.Error `json:"error"`
			}{2, typed})
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		var remote *cli.ExitError
		if errors.As(err, &remote) {
			os.Exit(remote.Code)
		}
		os.Exit(1)
	}
}
