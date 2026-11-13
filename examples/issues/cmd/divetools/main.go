// Divetools writes the tool files of the issues example to .dive/tools,
// from the tools that [issues.DiveTools] defines. It is run by
// go generate in examples/issues.
//
// Usage:
//
//	go run ./cmd/divetools
package main

import (
	"log"

	"github.com/loon-rocks/dive-go/examples/issues"
)

func main() {
	if err := issues.DiveTools(issues.NewStore(), nil).Generate(".dive/tools"); err != nil {
		log.Fatal(err)
	}
}
