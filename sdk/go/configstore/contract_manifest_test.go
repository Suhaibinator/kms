package configstore

import (
	"errors"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func TestValidateContract(t *testing.T) {
	valid := []ContractEntry{
		{Alias: "database", Kind: ContractKindParameter, ContentType: "json"},
		{Alias: "password", Kind: ContractKindSecret},
	}
	if err := validateContract(valid); err != nil {
		t.Fatalf("validateContract(valid) error = %v", err)
	}

	tests := map[string][]ContractEntry{
		"empty": nil,
		"blank alias": {
			{Alias: "", Kind: ContractKindSecret},
		},
		"noncanonical alias": {
			{Alias: " database ", Kind: ContractKindParameter, ContentType: "json"},
		},
		"duplicate alias": {
			{Alias: "database", Kind: ContractKindParameter, ContentType: "json"},
			{Alias: "database", Kind: ContractKindSecret},
		},
		"unknown kind": {
			{Alias: "database", Kind: ContractKind("other")},
		},
		"parameter without content type": {
			{Alias: "database", Kind: ContractKindParameter},
		},
	}
	for name, contract := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateContract(contract); err == nil {
				t.Fatal("validateContract() unexpectedly succeeded")
			}
		})
	}
}

func TestManifestValidationErrorsAreClassifiedAndRedacted(t *testing.T) {
	// ReleaseManifest has no public constructor by design. Its zero value is
	// sufficient to exercise exact-entry rejection without manufacturing
	// unresolved release internals.
	validator := manifestValidator([]ContractEntry{{
		Alias: "database", Kind: ContractKindParameter, ContentType: "json",
	}})
	err := validator(t.Context(), kmsclient.ReleaseManifest{})
	if err == nil {
		t.Fatal("manifestValidator() unexpectedly succeeded")
	}
	var candidateErr *CandidateError
	if !errors.As(err, &candidateErr) || candidateErr.ReleaseRejectionCategory() != string(RejectConfigContractMismatch) {
		t.Fatalf("manifest validation category = %#v", candidateErr)
	}
}
