// Package configgen implements the development-time generator behind
// cmd/kms-config-gen.
package configgen

import (
	"fmt"
	"go/token"
	"go/types"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	kmsclientPath   = "github.com/Suhaibinator/kms/sdk/go/kmsclient"
	configstorePath = "github.com/Suhaibinator/kms/sdk/go/configstore"
)

var aliasPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

const maxReleaseEntries = 256

type ir struct {
	PackagePath    string
	PackageName    string
	TypeName       string
	Root           *types.Named
	Groups         []*groupIR
	Secrets        []*fieldIR
	Fields         []*fieldIR
	Views          []*viewIR
	InlinePointers []string
}

type groupIR struct {
	Alias  string
	Fields []*fieldIR
}

type viewIR struct {
	Name   string
	Method string
	Fields []*fieldIR
}

type fieldIR struct {
	GoName   string
	GoPath   string
	JSONName string
	Source   string
	Secret   bool
	Reload   string
	Views    []string
	Type     *typeIR
	Position token.Pos
	Index    []int
}

func (f *fieldIR) canonicalName() string {
	if f.Secret {
		return f.Source
	}
	return f.Source + "." + f.JSONName
}

type typeKind uint8

const (
	typeInvalid typeKind = iota
	typeBool
	typeString
	typeInt
	typeUint
	typeFloat
	typeDuration
	typePointer
	typeBytes
	typeArray
	typeSlice
	typeMap
	typeStruct
)

type typeIR struct {
	Kind       typeKind
	GoType     types.Type
	Elem       *typeIR
	Fields     []*nestedFieldIR
	Len        int64
	Bits       int
	Named      bool
	Mutable    bool
	Encoding   string
	SchemaType string
}

type nestedFieldIR struct {
	GoName   string
	JSONName string
	Included bool
	Type     *typeIR
	Index    int
}

func analyzePackage(pkg *types.Package, sizes types.Sizes, typeName string) (*ir, error) {
	if pkg == nil {
		return nil, fmt.Errorf("configgen: package type information is unavailable")
	}
	typeName = strings.TrimSpace(typeName)
	if typeName == "" {
		return nil, fmt.Errorf("configgen: -type is required")
	}
	obj := pkg.Scope().Lookup(typeName)
	if obj == nil {
		return nil, fmt.Errorf("configgen: type %s was not found in package %s", typeName, pkg.Path())
	}
	typeObj, ok := obj.(*types.TypeName)
	if !ok || typeObj.IsAlias() {
		return nil, fmt.Errorf("configgen: %s must name a defined struct type", typeName)
	}
	if !typeObj.Exported() {
		return nil, fmt.Errorf("configgen: root type %s must be exported for use by a separate binding package", typeName)
	}
	named, ok := types.Unalias(typeObj.Type()).(*types.Named)
	if !ok {
		return nil, fmt.Errorf("configgen: %s must name a defined struct type", typeName)
	}
	if named.TypeParams() != nil && named.TypeParams().Len() != 0 {
		return nil, fmt.Errorf("configgen: root type %s must not be generic", typeName)
	}
	rootStruct, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil, fmt.Errorf("configgen: root type %s must be a struct", typeName)
	}
	if err := validateMethod(named); err != nil {
		return nil, err
	}
	if sizes == nil {
		sizes = types.SizesFor("gc", "amd64")
	}

	result := &ir{PackagePath: pkg.Path(), PackageName: pkg.Name(), TypeName: typeName, Root: named}
	groupByAlias := make(map[string]*groupIR)
	secretAliases := make(map[string]string)
	jsonByGroup := make(map[string]map[string]string)
	viewByName := make(map[string]*viewIR)
	viewFieldByName := make(map[string]map[string]string)
	viewMethods := map[string]string{
		"Config":  "the generated Snapshot.Config method",
		"Release": "the generated Snapshot.Release method",
	}
	inlineStack := make(map[types.Type]bool)

	var walkStruct func(*types.Struct, []string, []int) error
	walkStruct = func(current *types.Struct, parentPath []string, parentIndex []int) error {
		for i := 0; i < current.NumFields(); i++ {
			field := current.Field(i)
			path := append(append([]string(nil), parentPath...), field.Name())
			index := append(append([]int(nil), parentIndex...), i)
			goPath := strings.Join(path, ".")
			location := "root field " + goPath
			if len(parentPath) != 0 {
				location = "inline field " + goPath
			}
			if !field.Exported() {
				return fmt.Errorf("configgen: unexported %s cannot be isolated by a separate binding package", location)
			}

			tag := reflect.StructTag(current.Tag(i))
			kmsTag, hasKMS := tag.Lookup("kms")
			trimmedKMS := strings.TrimSpace(kmsTag)
			if trimmedKMS == "inline" {
				if views, ok := tag.Lookup("kms_views"); ok && strings.TrimSpace(views) != "" {
					return fmt.Errorf("configgen: inline field %s must not declare kms_views", goPath)
				}
				inlineType := types.Unalias(field.Type())
				pointer := false
				if ptr, ok := inlineType.(*types.Pointer); ok {
					pointer = true
					inlineType = types.Unalias(ptr.Elem())
				}
				named, ok := inlineType.(*types.Named)
				if !ok || named.Obj() == nil || !named.Obj().Exported() || named.TypeParams() != nil && named.TypeParams().Len() != 0 {
					return fmt.Errorf("configgen: inline field %s must have an exported, non-generic named struct type or pointer to one", goPath)
				}
				inlineStruct, ok := named.Underlying().(*types.Struct)
				if !ok {
					return fmt.Errorf("configgen: inline field %s must have a struct type", goPath)
				}
				if inlineStack[named] {
					return fmt.Errorf("configgen: inline field %s creates a recursive inline configuration", goPath)
				}
				if pointer {
					result.InlinePointers = append(result.InlinePointers, goPath)
				}
				before := len(result.Fields)
				inlineStack[named] = true
				err := walkStruct(inlineStruct, path, index)
				delete(inlineStack, named)
				if err != nil {
					return err
				}
				if len(result.Fields) == before {
					return fmt.Errorf("configgen: inline field %s contains no managed fields", goPath)
				}
				continue
			}

			if field.Anonymous() {
				return fmt.Errorf("configgen: embedded field %s must declare kms:\"inline\"", goPath)
			}
			if !hasKMS || trimmedKMS == "" {
				return fmt.Errorf("configgen: exported %s must declare kms:\"-\", kms:\"inline\", or one managed source", location)
			}
			if trimmedKMS == "-" {
				if views, ok := tag.Lookup("kms_views"); ok && strings.TrimSpace(views) != "" {
					return fmt.Errorf("configgen: excluded field %s must not declare kms_views", goPath)
				}
				if _, err := analyzeType(field.Type(), sizes, make(map[types.Type]bool), "excluded field "+goPath); err != nil {
					return fmt.Errorf("configgen: excluded field %s must be structurally deep-cloneable: %w", goPath, err)
				}
				continue
			}

			clauses, err := parseKMSClauses(goPath, kmsTag)
			if err != nil {
				return err
			}
			groupAlias, hasGroup := clauses["group"]
			secretAlias, hasSecret := clauses["secret"]
			if hasGroup == hasSecret {
				return fmt.Errorf("configgen: field %s must declare exactly one of group or secret", goPath)
			}
			reload, ok := clauses["reload"]
			if !ok || (reload != "hot" && reload != "restart") {
				return fmt.Errorf("configgen: field %s must declare reload=hot or reload=restart", goPath)
			}
			views, err := parseViews(goPath, tag)
			if err != nil {
				return err
			}

			managed := &fieldIR{GoName: field.Name(), GoPath: goPath, Reload: reload, Views: views, Position: field.Pos(), Index: index}
			if hasSecret {
				if !validAlias(secretAlias) {
					return fmt.Errorf("configgen: field %s has invalid secret alias %q", goPath, secretAlias)
				}
				if !isNamedType(field.Type(), kmsclientPath, "Secret") {
					return fmt.Errorf("configgen: secret field %s must have exact type kmsclient.Secret", goPath)
				}
				jsonTag, ok := tag.Lookup("json")
				if !ok || jsonTag != "-" {
					return fmt.Errorf("configgen: secret field %s must declare json:\"-\"", goPath)
				}
				if previous, duplicate := secretAliases[secretAlias]; duplicate {
					return fmt.Errorf("configgen: secret alias %q is used by both %s and %s", secretAlias, previous, goPath)
				}
				if _, collision := groupByAlias[secretAlias]; collision {
					return fmt.Errorf("configgen: alias %q is used as both a group and a secret", secretAlias)
				}
				secretAliases[secretAlias] = goPath
				managed.Source = secretAlias
				managed.Secret = true
				result.Secrets = append(result.Secrets, managed)
			} else {
				if !validAlias(groupAlias) {
					return fmt.Errorf("configgen: field %s has invalid group alias %q", goPath, groupAlias)
				}
				if _, collision := secretAliases[groupAlias]; collision {
					return fmt.Errorf("configgen: alias %q is used as both a group and a secret", groupAlias)
				}
				if isNamedType(field.Type(), kmsclientPath, "ParameterValue") || isNamedType(field.Type(), kmsclientPath, "SecretValue") {
					return fmt.Errorf("configgen: field %s uses legacy managed type %s; managed config fields must use ordinary values", goPath, types.TypeString(field.Type(), nil))
				}
				if isNamedType(field.Type(), kmsclientPath, "Secret") {
					return fmt.Errorf("configgen: kmsclient.Secret field %s must use secret=, not group=", goPath)
				}
				jsonName, err := explicitJSONName(goPath, tag)
				if err != nil {
					return err
				}
				typeInfo, err := analyzeType(field.Type(), sizes, make(map[types.Type]bool), "field "+goPath)
				if err != nil {
					return err
				}
				managed.Source = groupAlias
				managed.JSONName = jsonName
				managed.Type = typeInfo
				group := groupByAlias[groupAlias]
				if group == nil {
					group = &groupIR{Alias: groupAlias}
					groupByAlias[groupAlias] = group
					jsonByGroup[groupAlias] = make(map[string]string)
				}
				if previous, duplicate := jsonByGroup[groupAlias][jsonName]; duplicate {
					return fmt.Errorf("configgen: JSON name %q in group %q is used by both %s and %s", jsonName, groupAlias, previous, goPath)
				}
				jsonByGroup[groupAlias][jsonName] = goPath
				group.Fields = append(group.Fields, managed)
			}
			result.Fields = append(result.Fields, managed)

			for _, viewName := range views {
				view := viewByName[viewName]
				if view == nil {
					method := exportedIdentifier(viewName)
					if previous, collision := viewMethods[method]; collision {
						return fmt.Errorf("configgen: view %q generates method %s, which collides with %s", viewName, method, previous)
					}
					viewMethods[method] = fmt.Sprintf("view %q", viewName)
					view = &viewIR{Name: viewName, Method: method}
					viewByName[viewName] = view
					viewFieldByName[viewName] = make(map[string]string)
				}
				if previous, collision := viewFieldByName[viewName][managed.GoName]; collision {
					return fmt.Errorf("configgen: view %q getter %s is used by both %s and %s", viewName, managed.GoName, previous, goPath)
				}
				viewFieldByName[viewName][managed.GoName] = goPath
				view.Fields = append(view.Fields, managed)
			}
		}
		return nil
	}

	inlineStack[named] = true
	if err := walkStruct(rootStruct, nil, nil); err != nil {
		return nil, err
	}
	delete(inlineStack, named)
	if len(result.Fields) == 0 {
		return nil, fmt.Errorf("configgen: root type %s has no managed fields", typeName)
	}
	for _, group := range groupByAlias {
		sort.Slice(group.Fields, func(i, j int) bool { return group.Fields[i].JSONName < group.Fields[j].JSONName })
		result.Groups = append(result.Groups, group)
	}
	sort.Slice(result.Groups, func(i, j int) bool { return result.Groups[i].Alias < result.Groups[j].Alias })
	sort.Slice(result.Secrets, func(i, j int) bool { return result.Secrets[i].Source < result.Secrets[j].Source })
	sort.Slice(result.Fields, func(i, j int) bool { return result.Fields[i].canonicalName() < result.Fields[j].canonicalName() })
	for _, view := range viewByName {
		sort.Slice(view.Fields, func(i, j int) bool { return view.Fields[i].canonicalName() < view.Fields[j].canonicalName() })
		result.Views = append(result.Views, view)
	}
	sort.Slice(result.Views, func(i, j int) bool { return result.Views[i].Name < result.Views[j].Name })
	if len(result.Groups)+len(result.Secrets) > maxReleaseEntries {
		return nil, fmt.Errorf("configgen: contract requires %d release entries; maximum is %d", len(result.Groups)+len(result.Secrets), maxReleaseEntries)
	}
	return result, nil
}

func validateMethod(root *types.Named) error {
	method := types.NewMethodSet(types.NewPointer(root)).Lookup(nil, "Validate")
	if method == nil {
		return fmt.Errorf("configgen: *%s must implement Validate() error", root.Obj().Name())
	}
	if len(method.Index()) != 1 {
		return fmt.Errorf("configgen: *%s must declare Validate() error directly; a promoted inline-fragment method is not aggregate validation", root.Obj().Name())
	}
	sig, ok := method.Obj().Type().(*types.Signature)
	if !ok || sig.Params().Len() != 0 || sig.Results().Len() != 1 || sig.Variadic() || !types.Identical(sig.Results().At(0).Type(), types.Universe.Lookup("error").Type()) {
		return fmt.Errorf("configgen: *%s Validate method must have signature Validate() error", root.Obj().Name())
	}
	receiver := types.Unalias(sig.Recv().Type())
	if pointer, ok := receiver.(*types.Pointer); ok {
		receiver = types.Unalias(pointer.Elem())
	}
	if !types.Identical(receiver, root) {
		return fmt.Errorf("configgen: *%s must declare Validate() error directly", root.Obj().Name())
	}
	return nil
}

func parseKMSClauses(fieldName, raw string) (map[string]string, error) {
	result := make(map[string]string)
	for _, rawClause := range strings.Split(raw, ",") {
		clause := strings.TrimSpace(rawClause)
		if clause == "" {
			return nil, fmt.Errorf("configgen: field %s has an empty kms tag clause", fieldName)
		}
		key, value, ok := strings.Cut(clause, "=")
		if !ok || strings.TrimSpace(key) != key || strings.TrimSpace(value) != value || value == "" {
			return nil, fmt.Errorf("configgen: field %s has malformed kms tag clause %q", fieldName, clause)
		}
		if key != "group" && key != "secret" && key != "reload" {
			return nil, fmt.Errorf("configgen: field %s has unknown kms tag clause %q", fieldName, key)
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("configgen: field %s has duplicate kms tag clause %q", fieldName, key)
		}
		result[key] = value
	}
	return result, nil
}

func parseViews(fieldName string, tag reflect.StructTag) ([]string, error) {
	raw, ok := tag.Lookup("kms_views")
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("configgen: field %s must declare at least one kms_views membership", fieldName)
	}
	seen := make(map[string]bool)
	var result []string
	for _, item := range strings.Split(raw, ",") {
		name := strings.TrimSpace(item)
		if !validAlias(name) {
			return nil, fmt.Errorf("configgen: field %s has invalid view name %q", fieldName, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("configgen: field %s has duplicate view %q", fieldName, name)
		}
		seen[name] = true
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func explicitJSONName(fieldName string, tag reflect.StructTag) (string, error) {
	raw, ok := tag.Lookup("json")
	if !ok {
		return "", fmt.Errorf("configgen: parameter field %s must declare an explicit json name", fieldName)
	}
	parts := strings.Split(raw, ",")
	if parts[0] == "" || parts[0] == "-" {
		return "", fmt.Errorf("configgen: parameter field %s must declare a nonempty JSON property name", fieldName)
	}
	if len(parts) != 1 {
		return "", fmt.Errorf("configgen: parameter field %s has unsupported json tag options", fieldName)
	}
	if !validAlias(parts[0]) {
		return "", fmt.Errorf("configgen: parameter field %s has noncanonical JSON property name %q", fieldName, parts[0])
	}
	return parts[0], nil
}

func validAlias(alias string) bool { return aliasPattern.MatchString(alias) }

func exportedIdentifier(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' })
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		runes := []rune(part)
		runes[0] = unicode.ToUpper(runes[0])
		b.WriteString(string(runes))
	}
	return b.String()
}

func isNamedType(t types.Type, pkgPath, name string) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj() != nil && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == pkgPath && named.Obj().Name() == name
}

func analyzeType(t types.Type, sizes types.Sizes, stack map[types.Type]bool, location string) (*typeIR, error) {
	t = types.Unalias(t)
	if isNamedType(t, kmsclientPath, "ParameterValue") || isNamedType(t, kmsclientPath, "SecretValue") {
		return nil, fmt.Errorf("configgen: %s uses unsupported legacy managed type %s", location, types.TypeString(t, nil))
	}
	if isNamedType(t, kmsclientPath, "Secret") {
		return nil, fmt.Errorf("configgen: %s contains kmsclient.Secret below the root; secrets must be direct root fields", location)
	}
	if isNamedType(t, "time", "Duration") {
		return &typeIR{Kind: typeDuration, GoType: t, Bits: 64, Named: true, Encoding: "go-duration", SchemaType: "string"}, nil
	}

	named := false
	underlying := t
	if n, ok := t.(*types.Named); ok {
		named = true
		if n.Obj() != nil && !n.Obj().Exported() {
			return nil, fmt.Errorf("configgen: %s uses unexported named type %s, which a separate binding package cannot name", location, n.Obj().Name())
		}
		if n.TypeParams() != nil && n.TypeParams().Len() != 0 {
			return nil, fmt.Errorf("configgen: %s uses unsupported generic type %s", location, types.TypeString(t, nil))
		}
		underlying = n.Underlying()
	}

	if basic, ok := underlying.(*types.Basic); ok {
		info := basic.Info()
		bits := int(sizes.Sizeof(t) * 8)
		// Machine-sized integers use a fixed portable 32-bit contract. This keeps
		// checked-in schema/contract artifacts deterministic across GOARCH and
		// guarantees that every schema-valid value fits the generated field on
		// both 32-bit and 64-bit Go targets.
		if basic.Kind() == types.Int || basic.Kind() == types.Uint {
			bits = 32
		}
		switch {
		case info&types.IsBoolean != 0:
			return &typeIR{Kind: typeBool, GoType: t, Named: named, Encoding: "boolean", SchemaType: "boolean"}, nil
		case info&types.IsString != 0:
			return &typeIR{Kind: typeString, GoType: t, Named: named, Encoding: "string", SchemaType: "string"}, nil
		case info&types.IsInteger != 0 && info&types.IsUnsigned == 0:
			return &typeIR{Kind: typeInt, GoType: t, Named: named, Bits: bits, Encoding: fmt.Sprintf("int%d", bits), SchemaType: "integer"}, nil
		case info&types.IsInteger != 0 && info&types.IsUnsigned != 0 && basic.Kind() != types.Uintptr:
			return &typeIR{Kind: typeUint, GoType: t, Named: named, Bits: bits, Encoding: fmt.Sprintf("uint%d", bits), SchemaType: "integer"}, nil
		case info&types.IsFloat != 0:
			return &typeIR{Kind: typeFloat, GoType: t, Named: named, Bits: bits, Encoding: fmt.Sprintf("float%d", bits), SchemaType: "number"}, nil
		default:
			return nil, fmt.Errorf("configgen: %s has unsupported scalar type %s", location, types.TypeString(t, nil))
		}
	}

	if stack[t] {
		return nil, fmt.Errorf("configgen: %s has recursive type %s", location, types.TypeString(t, nil))
	}
	stack[t] = true
	defer delete(stack, t)

	switch value := underlying.(type) {
	case *types.Pointer:
		if named {
			return nil, fmt.Errorf("configgen: %s uses a defined pointer type; pointer-to-scalar fields must use an ordinary pointer", location)
		}
		elem, err := analyzeType(value.Elem(), sizes, stack, location)
		if err != nil {
			return nil, err
		}
		if !elem.isScalar() {
			return nil, fmt.Errorf("configgen: %s uses pointer to non-scalar type %s", location, types.TypeString(value.Elem(), nil))
		}
		return &typeIR{Kind: typePointer, GoType: t, Elem: elem, Named: named, Mutable: true, Encoding: "pointer-" + elem.Encoding, SchemaType: elem.SchemaType}, nil
	case *types.Slice:
		if types.Identical(types.Unalias(value.Elem()), types.Typ[types.Uint8]) {
			return &typeIR{Kind: typeBytes, GoType: t, Named: named, Mutable: true, Encoding: "base64", SchemaType: "string"}, nil
		}
		elem, err := analyzeType(value.Elem(), sizes, stack, location+" element")
		if err != nil {
			return nil, err
		}
		return &typeIR{Kind: typeSlice, GoType: t, Elem: elem, Named: named, Mutable: true, Encoding: "array", SchemaType: "array"}, nil
	case *types.Array:
		elem, err := analyzeType(value.Elem(), sizes, stack, location+" element")
		if err != nil {
			return nil, err
		}
		return &typeIR{Kind: typeArray, GoType: t, Elem: elem, Len: value.Len(), Named: named, Mutable: elem.Mutable, Encoding: "array", SchemaType: "array"}, nil
	case *types.Map:
		keyBasic, ok := types.Unalias(value.Key()).Underlying().(*types.Basic)
		if !ok || keyBasic.Info()&types.IsString == 0 {
			return nil, fmt.Errorf("configgen: %s has map with unsupported key type %s; map keys must be strings", location, types.TypeString(value.Key(), nil))
		}
		elem, err := analyzeType(value.Elem(), sizes, stack, location+" map value")
		if err != nil {
			return nil, err
		}
		return &typeIR{Kind: typeMap, GoType: t, Elem: elem, Named: named, Mutable: true, Encoding: "string-map", SchemaType: "object"}, nil
	case *types.Struct:
		result := &typeIR{Kind: typeStruct, GoType: t, Named: named, Encoding: "object", SchemaType: "object"}
		jsonNames := make(map[string]string)
		for i := 0; i < value.NumFields(); i++ {
			field := value.Field(i)
			if !field.Exported() {
				return nil, fmt.Errorf("configgen: %s contains unexported struct field %s; defensive copying would be ambiguous", location, field.Name())
			}
			if field.Anonymous() {
				return nil, fmt.Errorf("configgen: %s contains embedded field %s; embedded JSON fields are ambiguous", location, field.Name())
			}
			rawTag := reflect.StructTag(value.Tag(i)).Get("json")
			parts := strings.Split(rawTag, ",")
			if len(parts) > 1 {
				return nil, fmt.Errorf("configgen: %s.%s has unsupported json tag options", location, field.Name())
			}
			if rawTag == "-" {
				return nil, fmt.Errorf("configgen: %s.%s is excluded from JSON; every exported field in a managed composite must have a canonical encoding", location, field.Name())
			}
			included := rawTag != "-"
			jsonName := field.Name()
			if rawTag != "" && rawTag != "-" {
				jsonName = rawTag
			}
			if included {
				if !validAlias(jsonName) {
					return nil, fmt.Errorf("configgen: %s.%s has noncanonical JSON property name %q", location, field.Name(), jsonName)
				}
				if previous, duplicate := jsonNames[jsonName]; duplicate {
					return nil, fmt.Errorf("configgen: %s has duplicate JSON name %q on fields %s and %s", location, jsonName, previous, field.Name())
				}
				jsonNames[jsonName] = field.Name()
			}
			fieldType, err := analyzeType(field.Type(), sizes, stack, location+"."+field.Name())
			if err != nil {
				return nil, err
			}
			result.Fields = append(result.Fields, &nestedFieldIR{GoName: field.Name(), JSONName: jsonName, Included: included, Type: fieldType, Index: i})
			result.Mutable = result.Mutable || fieldType.Mutable
		}
		return result, nil
	default:
		return nil, fmt.Errorf("configgen: %s has unsupported type %s", location, types.TypeString(t, nil))
	}
}

func (t *typeIR) isScalar() bool {
	switch t.Kind {
	case typeBool, typeString, typeInt, typeUint, typeFloat, typeDuration:
		return true
	default:
		return false
	}
}
