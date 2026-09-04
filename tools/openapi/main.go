// Command openapi exports the product API without starting services or providers.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing/fstest"

	httpapi "github.com/OutboundSpade/LabCentral/api/http"
	"github.com/OutboundSpade/LabCentral/internal/controlplane"
)

func main() {
	server, err := httpapi.New(&controlplane.Service{}, []byte("example"), fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("LabCentral")}}, httpapi.Options{Authentication: httpapi.AuthenticationOptions{Mode: httpapi.AuthModeNone}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	b, err := json.MarshalIndent(server.API.OpenAPI(), "", "  ")
	if err == nil {
		err = os.WriteFile("api/http/openapi.json", append(b, '\n'), 0644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
