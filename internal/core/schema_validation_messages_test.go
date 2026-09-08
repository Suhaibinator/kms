package core

import (
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"math/big"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

func TestSchemaValidationMessagesDescribeMissingFieldsWithoutValues(t *testing.T) {
	schema, err := compileSchema(`{"type":"object","properties":{"oauth":{"type":"object","required":["client_id","redirect_uri"],"properties":{"token":{"type":"integer"}}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	err = schema.Validate(map[string]any{"oauth": map[string]any{"token": "private-token-do-not-show"}})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	problems := sanitizeSchemaErrors(err)
	if len(problems) != 2 {
		t.Fatalf("problems=%+v", problems)
	}
	var missing bool
	for _, problem := range problems {
		if problem.Alias != "oauth" {
			t.Fatalf("wrong alias: %+v", problem)
		}
		if strings.Contains(problem.Message, "private-token") {
			t.Fatal("configuration value leaked")
		}
		if problem.SchemaPointer == "/required" {
			missing = strings.Contains(problem.Message, `"client_id"`) && strings.Contains(problem.Message, `"redirect_uri"`)
		}
	}
	if !missing {
		t.Fatalf("missing actionable required fields: %+v", problems)
	}
}

func TestSchemaValidationMessagesDoNotEchoValidatorValues(t *testing.T) {
	const sensitive = "private-token-do-not-show"
	for _, rule := range []jsonschema.ErrorKind{&kind.Enum{Got: sensitive, Want: []any{sensitive}}, &kind.Pattern{Got: sensitive, Want: sensitive}} {
		message := actionableSchemaMessage(rule)
		if strings.Contains(message, sensitive) {
			t.Fatal("configuration value leaked")
		}
	}
}

func TestSchemaValidationMessagesDescribeBounds(t *testing.T) {
	got := actionableSchemaMessage(&kind.Minimum{Want: big.NewRat(5, 1), Got: big.NewRat(1, 1)})
	if got != "Use a value greater than or equal to 5." {
		t.Fatal(got)
	}
}
