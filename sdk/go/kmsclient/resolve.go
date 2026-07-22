package kmsclient

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

var (
	secretValueType = reflect.TypeOf(SecretValue{})
	paramValueType  = reflect.TypeOf(ParameterValue{})
)

// Resolve walks cfg (which must be a non-nil pointer to a struct) and
// initializes every SecretValue and ParameterValue field it finds. Fetches are
// issued concurrently to minimize startup latency.
//
// Walked: exported struct fields, non-nil pointers (including pointer chains),
// and the elements of slices and arrays — recursively, so a []SubConfig or
// []*SubConfig whose elements hold SecretValue/ParameterValue fields is fully
// initialized.
//
// Not walked: map values (dynamically keyed), interface values, unexported
// fields, and channels/funcs. A SecretValue/ParameterValue reached only through
// one of these is left uninitialized and will panic at .Value()/return "" from
// .Get(); place such fields where Resolve can reach them.
//
// If any field fails to resolve, Resolve returns the first error (after all
// in-flight fetches settle); already-initialized fields are left initialized.
func (c *Client) Resolve(ctx context.Context, cfg any) error {
	rv := reflect.ValueOf(cfg)
	if rv.Kind() != reflect.Pointer {
		return fmt.Errorf("kmsclient: Resolve requires a non-nil pointer to a struct, got %T", cfg)
	}
	if rv.IsNil() {
		return errors.New("kmsclient: Resolve called with a nil pointer")
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return fmt.Errorf("kmsclient: Resolve requires a pointer to a struct, got pointer to %s", elem.Kind())
	}

	var targets []initializer
	visited := make(map[uintptr]bool)
	collectTargets(elem, &targets, visited)

	if len(targets) == 0 {
		return nil
	}

	// The service exposes no batch-read RPC, so "batch into as few RPCs as
	// possible" (plan 9.5) means issuing the independent fetches concurrently.
	var wg sync.WaitGroup
	errs := make([]error, len(targets))
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t initializer) {
			defer wg.Done()
			errs[i] = t.init(ctx, c)
		}(i, t)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// initializer is the common Init surface of the declarative value types.
type initializer interface {
	init(ctx context.Context, c *Client) error
}

func (v *SecretValue) init(ctx context.Context, c *Client) error    { return v.InitContext(ctx, c) }
func (p *ParameterValue) init(ctx context.Context, c *Client) error { return p.InitContext(ctx, c) }

// collectTargets appends every addressable SecretValue/ParameterValue found in
// v to targets, recursing through structs, non-nil pointers (including pointer
// chains), and the elements of slices and arrays. Map values and interfaces are
// not walked. Non-addressable values are skipped, since Init needs a pointer.
func collectTargets(v reflect.Value, targets *[]initializer, visited map[uintptr]bool) {
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		if t == secretValueType {
			if v.CanAddr() {
				*targets = append(*targets, v.Addr().Interface().(*SecretValue))
			}
			return
		}
		if t == paramValueType {
			if v.CanAddr() {
				*targets = append(*targets, v.Addr().Interface().(*ParameterValue))
			}
			return
		}
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue // unexported
			}
			collectTargets(v.Field(i), targets, visited)
		}
	case reflect.Pointer:
		if v.IsNil() {
			break
		}
		// Guard against pointer cycles. Recurse into the pointee: a
		// *SecretValue / *ParameterValue lands in the Struct case above, whose
		// pointer we recover via Addr (Elem of a pointer is addressable). This
		// also transparently handles pointer chains (**T).
		addr := v.Pointer()
		if visited[addr] {
			break
		}
		visited[addr] = true
		collectTargets(v.Elem(), targets, visited)
	case reflect.Slice, reflect.Array:
		// Walk into slices/arrays of structs (and pointers to them), the common
		// config shape. Slice elements are always addressable; array elements
		// are addressable when the array itself is (it is, since we descend from
		// an addressable root), so the CanAddr guards above still hold.
		for i := 0; i < v.Len(); i++ {
			collectTargets(v.Index(i), targets, visited)
		}
	}
}
