// Command openapi exports the product API without starting services or providers.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	httpapi "github.com/OutboundSpade/LabCentral/api/http"
)

func main() {
	b, err := json.MarshalIndent(httpapi.OpenAPIDocument(), "", "  ")
	if err == nil {
		err = os.WriteFile("api/http/openapi.json", append(b, '\n'), 0644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
