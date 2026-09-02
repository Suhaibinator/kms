package metrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// eventTypeLiteral matches the dotted lower-case spelling every audit event
// type uses. Nothing else in core's non-test sources is spelled that way today;
// if something is one day, exclude it here rather than loosening the check.
var eventTypeLiteral = regexp.MustCompile(`^[a-z_]+(\.[a-z_]+)+$`)

// TestAuditEventTypesTrackCore keeps AuditEventTypes in step with the event
// types core actually writes. Core spells them inline at each call site, so
// the only way to notice a new one is to read the sources: every dotted string
// literal in internal/core must be in the allowlist, and every allowlist entry
// must still appear in core. A miss in the first direction means a new event
// type would be reported as "other"; a miss in the second means the list
// carries a series nothing can ever populate.
func TestAuditEventTypesTrackCore(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "core", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	inCore := map[string]string{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !eventTypeLiteral.MatchString(value) {
				return true
			}
			if _, seen := inCore[value]; !seen {
				inCore[value] = fset.Position(lit.Pos()).String()
			}
			return true
		})
	}
	if len(inCore) == 0 {
		t.Fatal("found no event-type literals in internal/core; is the test running from the package directory?")
	}

	for value, where := range inCore {
		if _, ok := auditEventTypeSet[value]; !ok {
			t.Errorf("%s: event type %q is not in AuditEventTypes (add it, or exclude the literal if it is not an event type)", where, value)
		}
	}
	for _, value := range AuditEventTypes {
		if _, ok := inCore[value]; !ok {
			t.Errorf("AuditEventTypes lists %q, which internal/core no longer writes", value)
		}
	}
}
