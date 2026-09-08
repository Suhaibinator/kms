package core

import (
	"context"
	"testing"
)

func TestRegisterBooleanJSONSchemas(t *testing.T) {
	svc, _ := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal(), "dev")
	for _, schema := range []string{"true", "false"} {
		t.Run(schema, func(t *testing.T) {
			if _, err := svc.CreateConfigurationSchema(context.Background(), adminPrincipal(), app.Name, schema, "{}"); err != nil {
				t.Fatalf("register valid boolean JSON schema: %v", err)
			}
		})
	}
}
