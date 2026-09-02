// Package envinject turns parameter-store entries into process environment
// variables for workloads that cannot use the SDK. It is a pure library: it
// maps names, encodes values, detects collisions, enforces size caps, and
// merges the result with a parent environment. Nothing here talks to the
// server, reads flags, or spawns processes; the CLI (`env` / `exec`) owns all
// of that.
//
// Naming rules:
//   - A store key ("billing/stripe-key") is uppercased with '/', '-' and '.'
//     folded to '_': BILLING_STRIPE_KEY.
//   - A release alias ("stripe-key") is uppercased with '-' folded to '_'.
//   - A name that would start with a digit is rejected unless the caller
//     supplies Rules.Prefix, which is prepended verbatim.
//
// Values are text unless they contain a NUL byte or are not valid UTF-8, in
// which case they are base64-encoded under "<NAME>_B64" and reported with a
// Note. Detection is content-based only: the store's content type carries no
// signal, since "application/octet-stream" is the default for every secret.
//
// Errors name keys, aliases, and variable names; they never contain a value.
package envinject

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// binarySuffix is appended to the variable name of a base64-encoded value.
const binarySuffix = "_B64"

// tokenEnvPrefix marks parent environment variables that carry a CLI
// authentication token. They are inputs to the CLI itself and are always
// stripped from a child's environment.
const tokenEnvPrefix = "KMS_SECRET_TOKEN_"

// Item is one store entry selected for injection.
type Item struct {
	Key         string // store key (namespace mode), e.g. "billing/stripe-key"; empty when Alias is set
	Alias       string // release alias (release mode); takes precedence over Key for naming
	Value       []byte
	ContentType string
	Secret      bool
}

// Var is one environment variable to inject.
type Var struct {
	Name   string
	Value  string
	Secret bool
}

// NoteKind classifies a Note.
type NoteKind string

// NoteBinaryEncoded reports a value that was emitted base64 under Name (which
// ends in _B64) because it is not printable text.
const NoteBinaryEncoded NoteKind = "binary_encoded"

// Note is a non-fatal observation about one item.
type Note struct {
	Kind NoteKind
	Key  string // source key or alias
	Name string // resulting variable name
}

// Rules configures Resolve.
type Rules struct {
	Prefix        string // validated with ValidPrefix; prepended verbatim
	MaxEntryBytes int    // 0 = unlimited; counts len(Name)+1+len(Value)
	MaxTotalBytes int    // 0 = unlimited; sum of entry bytes
}

// MapKey maps a store key to an environment variable name, without a prefix.
// It fails on an empty key, on characters a store key may not contain, and on
// a name that would start with a digit (use Resolve with Rules.Prefix for
// those).
func MapKey(key string) (string, error) {
	name, err := mapKey(key)
	if err != nil {
		return "", err
	}
	if startsWithDigit(name) {
		return "", digitError(key, name)
	}
	return name, nil
}

// MapAlias maps a release alias to an environment variable name, without a
// prefix. A valid alias always starts with a letter, so the result can never
// start with a digit.
func MapAlias(alias string) (string, error) {
	return mapAlias(alias)
}

// mapKey applies the key naming rules without the leading-digit check, which
// Resolve defers until the prefix has been applied.
func mapKey(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("key must not be empty")
	}
	if strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return "", fmt.Errorf("key %q must not have a leading or trailing slash", key)
	}
	var b strings.Builder
	b.Grow(len(key))
	for seg := range strings.SplitSeq(key, "/") {
		if b.Len() > 0 {
			b.WriteByte('_')
		}
		switch seg {
		case "":
			return "", fmt.Errorf("key %q has an empty segment", key)
		case ".", "..":
			return "", fmt.Errorf("key %q has a %q segment", key, seg)
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z':
				b.WriteRune(r - 'a' + 'A')
			case r >= '0' && r <= '9':
				b.WriteRune(r)
			case r == '-' || r == '.' || r == '_':
				b.WriteByte('_')
			default:
				return "", fmt.Errorf("key %q contains invalid character %q", key, r)
			}
		}
	}
	return b.String(), nil
}

// mapAlias applies the release-alias naming rules.
func mapAlias(alias string) (string, error) {
	if alias == "" {
		return "", fmt.Errorf("alias must not be empty")
	}
	var b strings.Builder
	b.Grow(len(alias))
	for i, r := range alias {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9', r == '-', r == '_':
			if i == 0 {
				return "", fmt.Errorf("alias %q must start with a letter", alias)
			}
			if r == '-' {
				b.WriteByte('_')
			} else {
				b.WriteRune(r)
			}
		default:
			return "", fmt.Errorf("alias %q contains invalid character %q", alias, r)
		}
	}
	return b.String(), nil
}

// ValidPrefix reports whether p is an acceptable --env-prefix. The empty
// string means "no prefix" and is valid.
func ValidPrefix(p string) bool {
	for i, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// IsBinary reports whether value must be base64-encoded to survive the
// environment: it contains a NUL byte (which terminates a C string) or is not
// valid UTF-8.
func IsBinary(value []byte) bool {
	return bytes.IndexByte(value, 0) >= 0 || !utf8.Valid(value)
}

// Resolve maps items to environment variables, sorted by name. A name that
// starts with a digit is accepted only when rules.Prefix is set, since the
// prefix is applied first and always begins with a letter or underscore.
// Binary values are base64-encoded under "<NAME>_B64" and reported as notes.
func Resolve(items []Item, rules Rules) ([]Var, []Note, error) {
	if !ValidPrefix(rules.Prefix) {
		return nil, nil, fmt.Errorf("invalid --env-prefix %q: must match [A-Za-z_][A-Za-z0-9_]*", rules.Prefix)
	}
	// One entry per item, carrying the source name so collision and note
	// messages can point back at the store.
	type entry struct {
		v      Var
		source string
		binary bool
	}
	entries := make([]entry, 0, len(items))
	sources := make(map[string]string, len(items))
	for _, item := range items {
		source := item.Key
		var name string
		var err error
		if item.Alias != "" {
			source = item.Alias
			name, err = mapAlias(item.Alias)
		} else {
			name, err = mapKey(item.Key)
		}
		if err != nil {
			return nil, nil, err
		}
		if rules.Prefix == "" && startsWithDigit(name) {
			return nil, nil, digitError(source, name)
		}
		name = rules.Prefix + name

		var value string
		binary := IsBinary(item.Value)
		if binary {
			name += binarySuffix
			value = base64.StdEncoding.EncodeToString(item.Value)
		} else {
			value = string(item.Value)
		}
		if prev, ok := sources[name]; ok {
			return nil, nil, fmt.Errorf("%q and %q both map to environment variable %s", prev, source, name)
		}
		sources[name] = source
		entries = append(entries, entry{
			v:      Var{Name: name, Value: value, Secret: item.Secret},
			source: source,
			binary: binary,
		})
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.v.Name, b.v.Name) })

	vars := make([]Var, 0, len(entries))
	var notes []Note
	total := 0
	for _, e := range entries {
		size := len(e.v.Name) + 1 + len(e.v.Value)
		if rules.MaxEntryBytes > 0 && size > rules.MaxEntryBytes {
			return nil, nil, fmt.Errorf("environment variable %s is %d bytes, over the %d-byte per-variable limit", e.v.Name, size, rules.MaxEntryBytes)
		}
		total += size
		vars = append(vars, e.v)
		if e.binary {
			notes = append(notes, Note{Kind: NoteBinaryEncoded, Key: e.source, Name: e.v.Name})
		}
	}
	if rules.MaxTotalBytes > 0 && total > rules.MaxTotalBytes {
		return nil, nil, fmt.Errorf("injected environment is %d bytes, over the %d-byte total limit", total, rules.MaxTotalBytes)
	}
	return vars, notes, nil
}

// Merge combines a parent environment (os.Environ form) with vars. Injected
// variables win by default; preserveParent flips that and reports the injected
// names the parent shadowed. Parent entries carrying a CLI token are always
// dropped, as are entries with no '='. caseInsensitive (Windows) compares
// names without regard to case, keeping the winning side's spelling and the
// first of any parent entries that differ only by case. The result lists the
// surviving parent entries in their original order, then the injected
// variables sorted by name.
func Merge(parent []string, vars []Var, preserveParent, caseInsensitive bool) (env []string, shadowed []string) {
	fold := func(name string) string {
		if caseInsensitive {
			return strings.ToUpper(name)
		}
		return name
	}

	// Injected variables are indexed by folded name; on a duplicate the first
	// in sorted order wins, so the output never repeats a name.
	injected := slices.Clone(vars)
	slices.SortFunc(injected, func(a, b Var) int { return strings.Compare(a.Name, b.Name) })
	index := make(map[string]int, len(injected))
	keep := make([]bool, len(injected))
	for i, v := range injected {
		key := fold(v.Name)
		if _, dup := index[key]; dup {
			continue
		}
		index[key] = i
		keep[i] = true
	}

	env = make([]string, 0, len(parent)+len(injected))
	seen := make(map[string]bool, len(parent))
	for _, entry := range parent {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue // not a "K=V" entry
		}
		if name == "" {
			// Windows per-drive working directories ("=C:=C:\\dir") have no
			// usable name; pass them through untouched.
			env = append(env, entry)
			continue
		}
		if hasTokenPrefix(name, caseInsensitive) {
			continue
		}
		key := fold(name)
		if seen[key] {
			continue // duplicate name in the parent; the first occurrence wins
		}
		seen[key] = true
		if i, ok := index[key]; ok {
			if !preserveParent {
				continue // the injected variable replaces this entry
			}
			keep[i] = false
			shadowed = append(shadowed, injected[i].Name)
		}
		env = append(env, entry)
	}
	for i, v := range injected {
		if keep[i] {
			env = append(env, v.Name+"="+v.Value)
		}
	}
	slices.Sort(shadowed)
	return env, shadowed
}

// hasTokenPrefix reports whether a parent variable name carries a CLI token.
func hasTokenPrefix(name string, caseInsensitive bool) bool {
	if caseInsensitive {
		return len(name) >= len(tokenEnvPrefix) && strings.EqualFold(name[:len(tokenEnvPrefix)], tokenEnvPrefix)
	}
	return strings.HasPrefix(name, tokenEnvPrefix)
}

// startsWithDigit reports whether name would be an illegal shell identifier
// because of its first character.
func startsWithDigit(name string) bool {
	return name != "" && name[0] >= '0' && name[0] <= '9'
}

// digitError explains that source maps to an unusable name and points at the
// flag that fixes it.
func digitError(source, name string) error {
	return fmt.Errorf("%q maps to %q, which starts with a digit: use --env-prefix to give it a usable name", source, name)
}
