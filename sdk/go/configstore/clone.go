package configstore

import (
	"reflect"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

var secretType = reflect.TypeFor[kmsclient.Secret]()

type cloneVisit struct {
	typ  reflect.Type
	kind reflect.Kind
	ptr  uintptr
	len  int
	cap  int
}

// Clone makes a recursive candidate-preparation copy of supported config
// values. In particular, kmsclient.Secret is copied through Secret.Clone so
// its plaintext backing buffer is never shared. Clone is intended for defaults,
// candidate construction, and defensive reports—not generated hot-path
// getters.
func Clone[T any](value T) T {
	cloned := cloneValue(reflect.ValueOf(value), make(map[cloneVisit]reflect.Value))
	if !cloned.IsValid() {
		var zero T
		return zero
	}
	return cloned.Interface().(T)
}

func cloneValue(value reflect.Value, seen map[cloneVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return reflect.Value{}
	}
	if value.Type() == secretType && value.CanInterface() {
		secret := value.Interface().(kmsclient.Secret).Clone()
		return reflect.ValueOf(secret)
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		item := cloneValue(value.Elem(), seen)
		out := reflect.New(value.Type()).Elem()
		if item.IsValid() {
			out.Set(item)
		}
		return out

	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if prior, ok := seen[visit]; ok {
			return prior
		}
		out := reflect.New(value.Type().Elem())
		seen[visit] = out
		out.Elem().Set(cloneValue(value.Elem(), seen))
		return out

	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Cap())
		if value.Len() > 0 {
			visit := cloneVisit{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer(), len: value.Len(), cap: value.Cap()}
			if prior, ok := seen[visit]; ok {
				return prior
			}
			seen[visit] = out
		}
		for i := range value.Len() {
			out.Index(i).Set(cloneValue(value.Index(i), seen))
		}
		return out

	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{typ: value.Type(), kind: value.Kind(), ptr: uintptr(value.UnsafePointer())}
		if prior, ok := seen[visit]; ok {
			return prior
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		seen[visit] = out
		iter := value.MapRange()
		for iter.Next() {
			key := cloneValue(iter.Key(), seen)
			item := cloneValue(iter.Value(), seen)
			out.SetMapIndex(key, item)
		}
		return out

	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := range value.Len() {
			out.Index(i).Set(cloneValue(value.Index(i), seen))
		}
		return out

	case reflect.Struct:
		// Begin with a value copy so unexported immutable implementation fields
		// (for example time.Time internals) are preserved. Recursively replace
		// every field reflection permits us to set.
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := range value.NumField() {
			if out.Field(i).CanSet() && value.Field(i).CanInterface() {
				out.Field(i).Set(cloneValue(value.Field(i), seen))
			}
		}
		return out

	default:
		return value
	}
}

func containsSecret(value any) bool {
	return valueContainsSecret(reflect.ValueOf(value), make(map[cloneVisit]struct{}))
}

func valueContainsSecret(value reflect.Value, seen map[cloneVisit]struct{}) bool {
	if !value.IsValid() {
		return false
	}
	if value.Type() == secretType {
		return true
	}

	switch value.Kind() {
	case reflect.Interface:
		return !value.IsNil() && valueContainsSecret(value.Elem(), seen)
	case reflect.Pointer:
		if value.IsNil() {
			return false
		}
		visit := cloneVisit{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if _, ok := seen[visit]; ok {
			return false
		}
		seen[visit] = struct{}{}
		return valueContainsSecret(value.Elem(), seen)
	case reflect.Slice:
		if value.IsNil() {
			return false
		}
		if value.Len() > 0 {
			visit := cloneVisit{typ: value.Type(), kind: value.Kind(), ptr: value.Pointer(), len: value.Len(), cap: value.Cap()}
			if _, ok := seen[visit]; ok {
				return false
			}
			seen[visit] = struct{}{}
		}
		for i := range value.Len() {
			if valueContainsSecret(value.Index(i), seen) {
				return true
			}
		}
	case reflect.Array:
		for i := range value.Len() {
			if valueContainsSecret(value.Index(i), seen) {
				return true
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return false
		}
		visit := cloneVisit{typ: value.Type(), kind: value.Kind(), ptr: uintptr(value.UnsafePointer())}
		if _, ok := seen[visit]; ok {
			return false
		}
		seen[visit] = struct{}{}
		iter := value.MapRange()
		for iter.Next() {
			if valueContainsSecret(iter.Key(), seen) || valueContainsSecret(iter.Value(), seen) {
				return true
			}
		}
	case reflect.Struct:
		for i := range value.NumField() {
			if valueContainsSecret(value.Field(i), seen) {
				return true
			}
		}
	}
	return false
}
