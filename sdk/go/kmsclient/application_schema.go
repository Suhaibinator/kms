package kmsclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"fmt"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// CreateApplicationSchemaOptions describes a schema registration. KMS derives
// the immutable release lineage from the named application.
type CreateApplicationSchemaOptions struct {
	Application  string
	Schema       []byte
	MetadataJSON string
}

// ApplicationSchema identifies a registered immutable schema version.
type ApplicationSchema struct {
	Application  string
	ReleaseName  string
	Version      uint64
	Digest       string
	MetadataJSON string
}

// CreateApplicationSchema registers a new schema version owned by an
// application. Registering the same compacted document twice returns
// ErrAlreadyExists.
func (c *Client) CreateApplicationSchema(ctx context.Context, opts CreateApplicationSchemaOptions) (ApplicationSchema, error) {
	if opts.Application == "" {
		return ApplicationSchema{}, fmt.Errorf("kmsclient: application is required")
	}
	if len(opts.Schema) == 0 {
		return ApplicationSchema{}, fmt.Errorf("kmsclient: schema is required")
	}
	compact := jsontext.Value(opts.Schema).Clone()
	if err := compact.Compact(jsontext.AllowDuplicateNames(false), jsontext.AllowInvalidUTF8(false)); err != nil {
		return ApplicationSchema{}, fmt.Errorf("kmsclient: schema must be valid JSON")
	}
	expectedDigest := sha256.Sum256(compact)
	expectedDigestHex := hex.EncodeToString(expectedDigest[:])
	callCtx, cancel := c.callCtx(ctx)
	defer cancel()
	response, err := c.schemas.CreateSchema(callCtx, &kmsv1.CreateSchemaRequest{
		Application:  opts.Application,
		SchemaJson:   string(opts.Schema),
		MetadataJson: opts.MetadataJSON,
	})
	if err != nil {
		return ApplicationSchema{}, mapError(err)
	}
	schema := response.GetSchema()
	if schema == nil || schema.GetApplication() != opts.Application || schema.GetReleaseName() == "" || schema.GetVersion() == 0 || !validSHA256Hex(schema.GetDigest()) || schema.GetDigest() != expectedDigestHex {
		return ApplicationSchema{}, fmt.Errorf("kmsclient: invalid schema response from server")
	}
	return ApplicationSchema{
		Application: schema.GetApplication(), ReleaseName: schema.GetReleaseName(),
		Version: schema.GetVersion(), Digest: schema.GetDigest(), MetadataJSON: schema.GetMetadataJson(),
	}, nil
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
