package main

import (
	"context"
	"fmt"
	"os"

	"github.com/OutboundSpade/LabCentral/tools/internal/sourceidentity"
)

func main() {
	digest, err := sourceidentity.Digest(context.Background(), ".", "./cmd/lab-provider-proxmox")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(digest)
}
