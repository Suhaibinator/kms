package configgen

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"
	"time"
)

// annotations carries the source-level documentation and application defaults
// that make the generated schema self-describing. Both are optional: a schema
// without them is still valid, only less helpful to the console's form editor.
type annotations struct {
	// docs maps a struct field's name position to its doc comment.
	docs map[token.Pos]string
	// rootDoc is the root type's doc comment.
	rootDoc string
	// defaults is the evaluated defaults literal keyed by Go field name; nil
	// when no defaults function was found or evaluated.
	defaults map[string]any
}

// unknownDefault marks a literal element the generator could not evaluate
// statically (a variable, a function call, an unsupported expression).
type unknownDefault struct{}

// nilDefault marks an explicit nil literal.
type nilDefault struct{}

// constDefault is a compile-time constant with its Go type.
type constDefault struct {
	value constant.Value
	typ   types.Type
}

// DefaultsFuncName is the defaults function the generator looks for when none
// is named explicitly.
const DefaultsFuncName = "Defaults"

func collectAnnotations(files []*ast.File, info *types.Info, rootType string, defaultsFunc string, explicit bool) (annotations, error) {
	result := annotations{docs: make(map[token.Pos]string)}
	for _, file := range files {
		if file == nil {
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			structType, ok := node.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				text := commentText(field.Doc)
				if text == "" {
					text = commentText(field.Comment)
				}
				if text == "" {
					continue
				}
				for _, name := range field.Names {
					result.docs[name.Pos()] = text
				}
			}
			return true
		})
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name == nil || typeSpec.Name.Name != rootType {
					continue
				}
				if text := commentText(typeSpec.Doc); text != "" {
					result.rootDoc = text
				} else if text := commentText(gen.Doc); text != "" {
					result.rootDoc = text
				}
			}
		}
	}
	if defaultsFunc == "-" {
		return result, nil
	}
	name := defaultsFunc
	if name == "" {
		name = DefaultsFuncName
	}
	decl := findFunc(files, name)
	if decl == nil {
		if explicit {
			return result, fmt.Errorf("configgen: defaults function %s was not found", name)
		}
		return result, nil
	}
	tree, err := evaluateDefaults(decl, info, rootType)
	if err != nil {
		if explicit {
			return result, err
		}
		return result, nil
	}
	result.defaults = tree
	return result, nil
}

func commentText(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}
	text := strings.TrimSpace(group.Text())
	if text == "" || strings.HasPrefix(text, "go:") || strings.HasPrefix(text, "nolint") {
		return ""
	}
	// Doc comments wrap at source width; the schema wants one line.
	return strings.Join(strings.Fields(text), " ")
}

func findFunc(files []*ast.File, name string) *ast.FuncDecl {
	for _, file := range files {
		if file == nil {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name == nil || fn.Name.Name != name {
				continue
			}
			return fn
		}
	}
	return nil
}

// evaluateDefaults reads a defaults function whose last statement is
//
//	return &Config{...}
//
// and returns the literal as a tree keyed by Go field names. A function that
// builds its value imperatively cannot be evaluated without running it, and
// guessing would put wrong defaults in the schema, so only the literal counts.
func evaluateDefaults(decl *ast.FuncDecl, info *types.Info, rootType string) (map[string]any, error) {
	name := decl.Name.Name
	if decl.Type.Params != nil && decl.Type.Params.NumFields() != 0 {
		return nil, fmt.Errorf("configgen: defaults function %s must take no parameters", name)
	}
	if decl.Type.Results == nil || decl.Type.Results.NumFields() != 1 {
		return nil, fmt.Errorf("configgen: defaults function %s must return exactly one value", name)
	}
	if decl.Body == nil || len(decl.Body.List) == 0 {
		return nil, fmt.Errorf("configgen: defaults function %s must end by returning a %s literal", name, rootType)
	}
	// Preceding statements (locals for pointer fields, say) are allowed; any
	// literal element that refers to them evaluates as unknown and gets no default.
	ret, ok := decl.Body.List[len(decl.Body.List)-1].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return nil, fmt.Errorf("configgen: defaults function %s must end by returning a %s literal", name, rootType)
	}
	expr := ast.Unparen(ret.Results[0])
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = ast.Unparen(unary.X)
	}
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("configgen: defaults function %s must return a %s composite literal", name, rootType)
	}
	litType := info.TypeOf(lit)
	if named, ok := types.Unalias(litType).(*types.Named); !ok || named.Obj() == nil || named.Obj().Name() != rootType {
		return nil, fmt.Errorf("configgen: defaults function %s must return a %s composite literal", name, rootType)
	}
	tree, ok := evalStructLiteral(lit, info)
	if !ok {
		return nil, fmt.Errorf("configgen: defaults function %s must use keyed fields in its %s literal", name, rootType)
	}
	return tree, nil
}

func evalStructLiteral(lit *ast.CompositeLit, info *types.Info) (map[string]any, bool) {
	tree := make(map[string]any, len(lit.Elts))
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil, false
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return nil, false
		}
		tree[key.Name] = evalExpr(kv.Value, info)
	}
	return tree, true
}

func evalExpr(expr ast.Expr, info *types.Info) any {
	expr = ast.Unparen(expr)
	if tv, ok := info.Types[expr]; ok {
		if tv.Value != nil {
			return constDefault{value: tv.Value, typ: tv.Type}
		}
		if tv.IsNil() {
			return nilDefault{}
		}
	}
	switch value := expr.(type) {
	case *ast.UnaryExpr:
		if value.Op == token.AND {
			return evalExpr(value.X, info)
		}
	case *ast.CompositeLit:
		underlying := info.TypeOf(value)
		if underlying == nil {
			return unknownDefault{}
		}
		switch t := types.Unalias(underlying).Underlying().(type) {
		case *types.Struct:
			tree, ok := evalStructLiteral(value, info)
			if !ok {
				return unknownDefault{}
			}
			return tree
		case *types.Slice, *types.Array:
			items := make([]any, 0, len(value.Elts))
			for _, elt := range value.Elts {
				if _, keyed := elt.(*ast.KeyValueExpr); keyed {
					return unknownDefault{}
				}
				items = append(items, evalExpr(elt, info))
			}
			return items
		case *types.Map:
			entries := make(map[string]any, len(value.Elts))
			for _, elt := range value.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					return unknownDefault{}
				}
				keyValue, ok := evalExpr(kv.Key, info).(constDefault)
				if !ok || keyValue.value.Kind() != constant.String {
					return unknownDefault{}
				}
				entries[constant.StringVal(keyValue.value)] = evalExpr(kv.Value, info)
			}
			return entries
		default:
			_ = t
		}
	}
	return unknownDefault{}
}

// schemaDefault converts a literal-tree value to the JSON default for a type.
// `raw == nil` means the field was omitted from the literal, i.e. Go's zero
// value. The boolean reports whether the default is fully known.
func schemaDefault(value *typeIR, raw any) (any, bool) {
	switch raw.(type) {
	case unknownDefault:
		return nil, false
	case nilDefault:
		raw = nil
	}
	switch value.Kind {
	case typeBool:
		if raw == nil {
			return false, true
		}
		if c, ok := raw.(constDefault); ok && c.value.Kind() == constant.Bool {
			return constant.BoolVal(c.value), true
		}
	case typeString:
		if raw == nil {
			return "", true
		}
		if c, ok := raw.(constDefault); ok && c.value.Kind() == constant.String {
			return constant.StringVal(c.value), true
		}
	case typeInt:
		if raw == nil {
			return int64(0), true
		}
		if c, ok := raw.(constDefault); ok {
			if n, exact := constant.Int64Val(constant.ToInt(c.value)); exact {
				return n, true
			}
		}
	case typeUint:
		if raw == nil {
			return uint64(0), true
		}
		if c, ok := raw.(constDefault); ok {
			if n, exact := constant.Uint64Val(constant.ToInt(c.value)); exact {
				return n, true
			}
		}
	case typeFloat:
		if raw == nil {
			return float64(0), true
		}
		if c, ok := raw.(constDefault); ok {
			if f, _ := constant.Float64Val(constant.ToFloat(c.value)); c.value.Kind() != constant.Unknown {
				return f, true
			}
		}
	case typeDuration:
		if raw == nil {
			return "0s", true
		}
		if c, ok := raw.(constDefault); ok {
			if n, exact := constant.Int64Val(constant.ToInt(c.value)); exact {
				return time.Duration(n).String(), true
			}
		}
	case typePointer:
		if raw == nil {
			return nil, true
		}
		return schemaDefault(value.Elem, raw)
	case typeBytes:
		if raw == nil {
			return nil, true
		}
	case typeSlice:
		if raw == nil {
			return nil, true
		}
		if items, ok := raw.([]any); ok {
			return schemaDefaultList(value.Elem, items)
		}
	case typeArray:
		if raw == nil {
			raw = make([]any, value.Len)
		}
		if items, ok := raw.([]any); ok && int64(len(items)) == value.Len {
			return schemaDefaultList(value.Elem, items)
		}
	case typeMap:
		if raw == nil {
			return nil, true
		}
		if entries, ok := raw.(map[string]any); ok {
			out := make(map[string]any, len(entries))
			for key, item := range entries {
				converted, known := schemaDefault(value.Elem, item)
				if !known {
					return nil, false
				}
				out[key] = converted
			}
			return out, true
		}
	case typeStruct:
		tree, _ := raw.(map[string]any)
		if raw != nil && tree == nil {
			return nil, false
		}
		out := make(map[string]any, len(value.Fields))
		for _, field := range value.Fields {
			if !field.Included {
				continue
			}
			var child any
			if tree != nil {
				child = tree[field.GoName]
			}
			converted, known := schemaDefault(field.Type, child)
			if !known {
				return nil, false
			}
			out[field.JSONName] = converted
		}
		return out, true
	}
	return nil, false
}

func schemaDefaultList(elem *typeIR, items []any) (any, bool) {
	out := make([]any, 0, len(items))
	for _, item := range items {
		converted, known := schemaDefault(elem, item)
		if !known {
			return nil, false
		}
		out = append(out, converted)
	}
	return out, true
}

// managedFieldDefault walks the defaults tree along a managed field's Go path
// (through inline structs) and returns the raw literal element for it, or nil
// when the literal omitted it.
func managedFieldDefault(tree map[string]any, goPath string) any {
	if tree == nil {
		return unknownDefault{}
	}
	var current any = tree
	for _, segment := range strings.Split(goPath, ".") {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[segment]
			if !ok {
				return nil
			}
			current = next
		case nil, nilDefault:
			// An omitted or nil inline struct leaves its fields at zero.
			return nil
		default:
			return unknownDefault{}
		}
	}
	return current
}
