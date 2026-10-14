// Generate the portable v3 authoring schema.
package main

import (
	lc "github.com/OutboundSpade/LabCentral/v2"
	"github.com/OutboundSpade/LabCentral/v2/instructor"
	"os"
)

func main() {
	b, e := lc.LabSchema()
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile("schemas/lab-v3.json", b, 0644); e != nil {
		panic(e)
	}
	b, e = instructor.Schema()
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile("schemas/instructor-v3.json", b, 0644); e != nil {
		panic(e)
	}
}
