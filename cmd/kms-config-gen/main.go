package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Suhaibinator/kms/internal/configgen"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("kms-config-gen", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var options configgen.Options
	var paths configgen.OutputPaths
	var check bool
	flags.StringVar(&options.Type, "type", "", "root configuration struct type")
	flags.StringVar(&options.Package, "package", ".", "Go package pattern containing the root type")
	flags.StringVar(&options.BindingPackage, "binding-package", "", "package name for the generated Go binding")
	flags.StringVar(&options.DefaultsFunc, "defaults", "", "package-level defaults function whose returned literal supplies schema defaults (default: Defaults when present; \"-\" disables)")
	flags.StringVar(&paths.Binding, "binding-output", "", "generated Go binding path")
	flags.StringVar(&paths.Schema, "schema-output", "", "generated JSON Schema path")
	flags.StringVar(&paths.Contract, "contract-output", "", "generated contract path")
	flags.BoolVar(&check, "check", false, "compare generated artifacts without writing")
	flags.BoolVar(&check, "verify", false, "alias for -check")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "kms-config-gen: positional arguments are not supported")
		return 2
	}
	artifacts, err := configgen.Generate(context.Background(), options)
	if err == nil {
		if check {
			err = configgen.Verify(paths, artifacts)
		} else {
			err = configgen.Write(paths, artifacts)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, configgen.ErrStale) {
			return 1
		}
		return 1
	}
	return 0
}
