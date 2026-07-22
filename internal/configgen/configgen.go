package configgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Options describes one generator invocation. Package defaults to the current
// package ("."). Dir controls the working directory used by go/packages.
type Options struct {
	Package        string
	Dir            string
	Type           string
	BindingPackage string
	Env            []string
}

// Artifacts contains the three complete, newline-terminated generated files.
// Binding has already been gofmt formatted.
type Artifacts struct {
	Binding  []byte
	Schema   []byte
	Contract []byte
}

// OutputPaths identifies the application-controlled artifact destinations.
type OutputPaths struct {
	Binding  string
	Schema   string
	Contract string
}

// ErrStale marks verification failures caused by missing or out-of-date files.
var ErrStale = errors.New("generated configuration artifacts are stale")

// StaleError lists every stale artifact rather than failing at the first file.
type StaleError struct{ Paths []string }

func (e *StaleError) Error() string {
	return fmt.Sprintf("configgen: %v: %s", ErrStale, strings.Join(e.Paths, ", "))
}

func (e *StaleError) Unwrap() error { return ErrStale }

// Generate loads and validates a root type, then renders all artifacts from a
// single normalized intermediate representation.
func Generate(ctx context.Context, options Options) (Artifacts, error) {
	if ctx == nil {
		return Artifacts{}, fmt.Errorf("configgen: context is required")
	}
	if strings.TrimSpace(options.BindingPackage) == "" {
		return Artifacts{}, fmt.Errorf("configgen: -binding-package is required")
	}
	if !token.IsIdentifier(options.BindingPackage) || token.Lookup(options.BindingPackage).IsKeyword() || options.BindingPackage == "_" {
		return Artifacts{}, fmt.Errorf("configgen: invalid binding package name %q", options.BindingPackage)
	}
	pattern := strings.TrimSpace(options.Package)
	if pattern == "" {
		pattern = "."
	}
	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
		packages.NeedDeps | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax |
		packages.NeedTypesSizes | packages.NeedModule
	cfg := &packages.Config{Context: ctx, Mode: mode, Dir: options.Dir}
	if options.Env != nil {
		cfg.Env = append([]string(nil), options.Env...)
	}
	loaded, err := packages.Load(cfg, pattern)
	if err != nil {
		return Artifacts{}, fmt.Errorf("configgen: load package %q: %w", pattern, err)
	}
	var candidates []*packages.Package
	var packageErrors []string
	for _, pkg := range loaded {
		if strings.HasSuffix(pkg.PkgPath, ".test") || strings.HasSuffix(pkg.ID, ".test") {
			continue
		}
		if len(pkg.Errors) != 0 {
			for _, packageErr := range pkg.Errors {
				packageErrors = append(packageErrors, packageErr.Error())
			}
			continue
		}
		if pkg.Types != nil {
			candidates = append(candidates, pkg)
		}
	}
	if len(packageErrors) != 0 {
		sort.Strings(packageErrors)
		return Artifacts{}, fmt.Errorf("configgen: package load failed:\n%s", strings.Join(packageErrors, "\n"))
	}
	if len(candidates) != 1 {
		return Artifacts{}, fmt.Errorf("configgen: package pattern %q resolved to %d packages; exactly one is required", pattern, len(candidates))
	}
	pkg := candidates[0]
	normalized, err := analyzePackage(pkg.Types, pkg.TypesSizes, options.Type)
	if err != nil {
		return Artifacts{}, err
	}
	schema, err := renderSchema(normalized)
	if err != nil {
		return Artifacts{}, err
	}
	contract, contractModel, err := renderContract(normalized, schema)
	if err != nil {
		return Artifacts{}, err
	}
	binding, err := renderBinding(normalized, options.BindingPackage, contractModel)
	if err != nil {
		return Artifacts{}, err
	}
	return Artifacts{Binding: binding, Schema: schema, Contract: contract}, nil
}

// Write writes all artifacts atomically. Existing files whose content already
// matches are left untouched.
func Write(paths OutputPaths, artifacts Artifacts) error {
	if err := validateOutputPaths(paths); err != nil {
		return err
	}
	for _, output := range []struct {
		name string
		path string
		data []byte
	}{
		{name: "binding", path: paths.Binding, data: artifacts.Binding},
		{name: "schema", path: paths.Schema, data: artifacts.Schema},
		{name: "contract", path: paths.Contract, data: artifacts.Contract},
	} {
		if strings.TrimSpace(output.path) == "" {
			return fmt.Errorf("configgen: %s output path is required", output.name)
		}
		current, err := os.ReadFile(output.path)
		if err == nil && bytes.Equal(current, output.data) {
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("configgen: read %s output %q: %w", output.name, output.path, err)
		}
		if err := atomicWrite(output.path, output.data); err != nil {
			return fmt.Errorf("configgen: write %s output %q: %w", output.name, output.path, err)
		}
	}
	return nil
}

// Verify compares generated artifacts without changing the filesystem.
func Verify(paths OutputPaths, artifacts Artifacts) error {
	if err := validateOutputPaths(paths); err != nil {
		return err
	}
	var stale []string
	for _, output := range []struct {
		name string
		path string
		data []byte
	}{
		{name: "binding", path: paths.Binding, data: artifacts.Binding},
		{name: "schema", path: paths.Schema, data: artifacts.Schema},
		{name: "contract", path: paths.Contract, data: artifacts.Contract},
	} {
		if strings.TrimSpace(output.path) == "" {
			return fmt.Errorf("configgen: %s output path is required", output.name)
		}
		current, err := os.ReadFile(output.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				stale = append(stale, output.path)
				continue
			}
			return fmt.Errorf("configgen: read %s output %q: %w", output.name, output.path, err)
		}
		if !bytes.Equal(current, output.data) {
			stale = append(stale, output.path)
		}
	}
	if len(stale) != 0 {
		sort.Strings(stale)
		return &StaleError{Paths: stale}
	}
	return nil
}

func validateOutputPaths(paths OutputPaths) error {
	named := []struct {
		name string
		path string
	}{{"binding", paths.Binding}, {"schema", paths.Schema}, {"contract", paths.Contract}}
	seen := make(map[string]string, len(named))
	for _, output := range named {
		if strings.TrimSpace(output.path) == "" {
			return fmt.Errorf("configgen: %s output path is required", output.name)
		}
		absolute, err := filepath.Abs(output.path)
		if err != nil {
			return fmt.Errorf("configgen: resolve %s output path: %w", output.name, err)
		}
		absolute = filepath.Clean(absolute)
		if previous, duplicate := seen[absolute]; duplicate {
			return fmt.Errorf("configgen: %s and %s outputs resolve to the same path %q", previous, output.name, absolute)
		}
		seen[absolute] = output.name
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".kms-config-gen-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	removeTemp = false
	return nil
}
