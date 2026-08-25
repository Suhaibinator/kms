package configstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultsArtifactFormat is the wire-format identity for generated defaults.
	DefaultsArtifactFormat = "kms-config-defaults/v1"

	// MaxDefaultsArtifactSize is the largest encoded artifact accepted by KMS.
	MaxDefaultsArtifactSize = 4 << 20

	// MaxDefaultsParameterValueSize is the largest exact encoded parameter value.
	MaxDefaultsParameterValueSize = 1 << 20
)

// DefaultsArtifact is a complete generated parameter baseline. Secret entries
// exist only in Contract; values can be represented only by Parameters.
type DefaultsArtifact struct {
	Format       string
	Profile      string
	SchemaSHA256 string
	Contract     []ContractEntry
	Parameters   []DefaultsParameter
}

// DefaultsParameter contains one exact encoded parameter value.
type DefaultsParameter struct {
	Alias       string
	ContentType string
	Value       string
}

type defaultsArtifactWire struct {
	Format       string                  `json:"format"`
	Profile      string                  `json:"profile"`
	SchemaSHA256 string                  `json:"schema_sha256"`
	Contract     []defaultsContractWire  `json:"contract"`
	Parameters   []defaultsParameterWire `json:"parameters"`
}

type defaultsArtifactDecodeWire struct {
	Format       *string                        `json:"format"`
	Profile      *string                        `json:"profile"`
	SchemaSHA256 *string                        `json:"schema_sha256"`
	Contract     *[]defaultsContractDecodeWire  `json:"contract"`
	Parameters   *[]defaultsParameterDecodeWire `json:"parameters"`
}

type defaultsContractWire struct {
	Alias       string       `json:"alias"`
	Kind        ContractKind `json:"kind"`
	ContentType string       `json:"content_type"`
}

type defaultsParameterWire struct {
	Alias       string `json:"alias"`
	ContentType string `json:"content_type"`
	Value       string `json:"value"`
}

type defaultsContractDecodeWire struct {
	Alias       *string       `json:"alias"`
	Kind        *ContractKind `json:"kind"`
	ContentType *string       `json:"content_type"`
}

type defaultsParameterDecodeWire struct {
	Alias       *string `json:"alias"`
	ContentType *string `json:"content_type"`
	Value       *string `json:"value"`
}

// EncodeDefaultsArtifact validates and deterministically encodes artifact.
// The returned JSON is compact and newline terminated.
func EncodeDefaultsArtifact(artifact DefaultsArtifact) ([]byte, error) {
	normalized, err := validateDefaultsArtifact(artifact, false)
	if err != nil {
		return nil, err
	}
	wire := defaultsArtifactWire{
		Format:       normalized.Format,
		Profile:      normalized.Profile,
		SchemaSHA256: normalized.SchemaSHA256,
		Contract:     make([]defaultsContractWire, len(normalized.Contract)),
		Parameters:   make([]defaultsParameterWire, len(normalized.Parameters)),
	}
	for i, entry := range normalized.Contract {
		wire.Contract[i] = defaultsContractWire(entry)
	}
	for i, parameter := range normalized.Parameters {
		wire.Parameters[i] = defaultsParameterWire(parameter)
	}

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(wire); err != nil {
		return nil, fmt.Errorf("configstore: encode defaults artifact: %w", err)
	}
	if output.Len() > MaxDefaultsArtifactSize {
		return nil, errors.New("configstore: defaults artifact exceeds 4 MiB")
	}
	return output.Bytes(), nil
}

// ParseDefaultsArtifact strictly parses and validates one defaults artifact.
// Unknown fields, trailing data, unsorted entries, and incomplete contracts are
// rejected. Parameter values are returned byte-for-byte as encoded in value.
func ParseDefaultsArtifact(data []byte) (DefaultsArtifact, error) {
	if len(data) > MaxDefaultsArtifactSize {
		return DefaultsArtifact{}, errors.New("configstore: defaults artifact exceeds 4 MiB")
	}
	if !utf8.Valid(data) {
		return DefaultsArtifact{}, errors.New("configstore: defaults artifact must be valid UTF-8")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return DefaultsArtifact{}, errors.New("configstore: defaults artifact is empty")
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return DefaultsArtifact{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire defaultsArtifactDecodeWire
	if err := decoder.Decode(&wire); err != nil {
		return DefaultsArtifact{}, fmt.Errorf("configstore: decode defaults artifact: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return DefaultsArtifact{}, err
	}
	if wire.Format == nil || wire.Profile == nil || wire.SchemaSHA256 == nil || wire.Contract == nil || wire.Parameters == nil {
		return DefaultsArtifact{}, errors.New("configstore: defaults artifact is missing a required field")
	}

	artifact := DefaultsArtifact{
		Format:       *wire.Format,
		Profile:      *wire.Profile,
		SchemaSHA256: *wire.SchemaSHA256,
		Contract:     make([]ContractEntry, len(*wire.Contract)),
		Parameters:   make([]DefaultsParameter, len(*wire.Parameters)),
	}
	for i, entry := range *wire.Contract {
		if entry.Alias == nil || entry.Kind == nil || entry.ContentType == nil {
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact contract entry is missing a required field")
		}
		artifact.Contract[i] = ContractEntry{Alias: *entry.Alias, Kind: *entry.Kind, ContentType: *entry.ContentType}
	}
	for i, parameter := range *wire.Parameters {
		if parameter.Alias == nil || parameter.ContentType == nil || parameter.Value == nil {
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact parameter entry is missing a required field")
		}
		artifact.Parameters[i] = DefaultsParameter{Alias: *parameter.Alias, ContentType: *parameter.ContentType, Value: *parameter.Value}
	}
	return validateDefaultsArtifact(artifact, true)
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanDefaultsJSONValue(decoder); err != nil {
		return fmt.Errorf("configstore: decode defaults artifact: %w", err)
	}
	return requireJSONEOF(decoder)
}

func scanDefaultsJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			rawName, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := rawName.(string)
			if !ok {
				return errors.New("object field name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return errors.New("duplicate JSON object field")
			}
			seen[name] = struct{}{}
			if err := scanDefaultsJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanDefaultsJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("array is not closed")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("configstore: defaults artifact contains trailing data")
		}
		return fmt.Errorf("configstore: decode defaults artifact trailing data: %w", err)
	}
	return nil
}

func validateDefaultsArtifact(artifact DefaultsArtifact, requireSorted bool) (DefaultsArtifact, error) {
	if artifact.Format != DefaultsArtifactFormat {
		return DefaultsArtifact{}, fmt.Errorf("configstore: defaults artifact format must be %q", DefaultsArtifactFormat)
	}
	if !canonicalDefaultsText(artifact.Profile, false) {
		return DefaultsArtifact{}, errors.New("configstore: defaults artifact profile must be nonempty and canonical")
	}
	if !validLowerSHA256(artifact.SchemaSHA256) {
		return DefaultsArtifact{}, errors.New("configstore: defaults artifact schema_sha256 must be 64 lowercase hexadecimal characters")
	}
	if len(artifact.Contract) == 0 {
		return DefaultsArtifact{}, errors.New("configstore: defaults artifact contract must not be empty")
	}

	contract := append([]ContractEntry(nil), artifact.Contract...)
	parameters := append([]DefaultsParameter(nil), artifact.Parameters...)
	if !requireSorted {
		sort.Slice(contract, func(i, j int) bool { return contract[i].Alias < contract[j].Alias })
		sort.Slice(parameters, func(i, j int) bool { return parameters[i].Alias < parameters[j].Alias })
	}

	parameterContract := make(map[string]ContractEntry, len(contract))
	for i, entry := range contract {
		if !validDefaultsAlias(entry.Alias) {
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact contains an invalid contract alias")
		}
		if i > 0 && contract[i-1].Alias >= entry.Alias {
			if contract[i-1].Alias == entry.Alias {
				return DefaultsArtifact{}, errors.New("configstore: defaults artifact contract contains a duplicate alias")
			}
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact contract must be sorted by alias")
		}
		switch entry.Kind {
		case ContractKindParameter:
			if !canonicalDefaultsText(entry.ContentType, false) {
				return DefaultsArtifact{}, errors.New("configstore: defaults artifact parameter contract entries require a canonical content type")
			}
			parameterContract[entry.Alias] = entry
		case ContractKindSecret:
			if !canonicalDefaultsText(entry.ContentType, true) {
				return DefaultsArtifact{}, errors.New("configstore: defaults artifact secret content types must be canonical")
			}
		default:
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact contract contains an invalid kind")
		}
	}

	seenParameters := make(map[string]struct{}, len(parameters))
	for i, parameter := range parameters {
		if !validDefaultsAlias(parameter.Alias) {
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact contains an invalid parameter alias")
		}
		if i > 0 && parameters[i-1].Alias >= parameter.Alias {
			if parameters[i-1].Alias == parameter.Alias {
				return DefaultsArtifact{}, errors.New("configstore: defaults artifact parameters contain a duplicate alias")
			}
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact parameters must be sorted by alias")
		}
		contractEntry, ok := parameterContract[parameter.Alias]
		if !ok {
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact parameter does not match the contract")
		}
		if parameter.ContentType != contractEntry.ContentType {
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact parameter content type does not match the contract")
		}
		if !utf8.ValidString(parameter.Value) {
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact parameter value must be valid UTF-8")
		}
		if len(parameter.Value) > MaxDefaultsParameterValueSize {
			return DefaultsArtifact{}, errors.New("configstore: defaults artifact parameter value exceeds 1 MiB")
		}
		seenParameters[parameter.Alias] = struct{}{}
	}
	if len(seenParameters) != len(parameterContract) {
		return DefaultsArtifact{}, errors.New("configstore: defaults artifact parameters do not completely match the contract")
	}

	artifact.Contract = contract
	artifact.Parameters = parameters
	return artifact, nil
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validDefaultsAlias(value string) bool {
	if len(value) == 0 || len(value) > 64 || !asciiLetter(value[0]) {
		return false
	}
	for i := 1; i < len(value); i++ {
		char := value[i]
		if !asciiLetter(char) && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func asciiLetter(char byte) bool {
	return char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
}

func canonicalDefaultsText(value string, allowEmpty bool) bool {
	if !allowEmpty && value == "" {
		return false
	}
	return utf8.ValidString(value) && strings.TrimSpace(value) == value
}
