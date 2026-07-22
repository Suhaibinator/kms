package configstore

import (
	"context"
	"errors"
	"strings"

	"github.com/Suhaibinator/kms/sdk/go/paramstore"
)

func validateContract(contract []ContractEntry) error {
	if len(contract) == 0 {
		return errors.New("configstore: Options.Contract must contain at least one entry")
	}
	seen := make(map[string]struct{}, len(contract))
	for _, entry := range contract {
		if entry.Alias == "" || strings.TrimSpace(entry.Alias) != entry.Alias {
			return errors.New("configstore: contract aliases must be non-empty and canonical")
		}
		if _, exists := seen[entry.Alias]; exists {
			return errors.New("configstore: contract contains a duplicate alias")
		}
		seen[entry.Alias] = struct{}{}
		switch entry.Kind {
		case ContractKindParameter:
			if entry.ContentType == "" {
				return errors.New("configstore: parameter contract entries require a content type")
			}
		case ContractKindSecret:
		default:
			return errors.New("configstore: contract entry kind is invalid")
		}
	}
	return nil
}

// manifestValidator is intentionally isolated in this file: it is the only
// configstore code coupled to paramstore's optional pre-resolution manifest
// validation hook.
func manifestValidator(contract []ContractEntry) paramstore.ValidateReleaseManifestFunc {
	want := make(map[string]ContractEntry, len(contract))
	for _, entry := range contract {
		want[entry.Alias] = entry
	}
	return func(ctx context.Context, manifest paramstore.ReleaseManifest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got := manifest.Entries()
		if len(got) != len(want) {
			return Reject(RejectConfigContractMismatch, errors.New("configstore: release aliases do not match generated contract"))
		}
		for alias, expected := range want {
			entry, ok := got[alias]
			if !ok || entry.Alias != alias || entry.Kind != string(expected.Kind) {
				return Reject(RejectConfigContractMismatch, errors.New("configstore: release entry does not match generated contract"))
			}
			if expected.ContentType != "" && entry.ContentType != expected.ContentType {
				return Reject(RejectConfigContractMismatch, errors.New("configstore: release content type does not match generated contract"))
			}
		}
		return nil
	}
}
