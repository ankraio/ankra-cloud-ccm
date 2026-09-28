package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	specificationPath := flag.String("specification", "../../api/openapi.yaml", "the OpenAPI document")
	outputPath := flag.String("output", "../../internal/ankraapi/operations_gen.go", "the generated Go file")
	packageName := flag.String("package", "ankraapi", "the Go package of the generated file")
	flag.Parse()
	specification, readError := os.ReadFile(*specificationPath)
	if readError != nil {
		fmt.Fprintln(os.Stderr, "openapigen:", readError)
		os.Exit(1)
	}
	generated, generateError := Generate(specification, *packageName)
	if generateError != nil {
		fmt.Fprintln(os.Stderr, "openapigen:", generateError)
		os.Exit(1)
	}
	if writeError := os.WriteFile(*outputPath, generated, 0o644); writeError != nil {
		fmt.Fprintln(os.Stderr, "openapigen:", writeError)
		os.Exit(1)
	}
}
