package domain

// ApplicationMigrationChange addresses a target contract alias. FromAlias maps
// an active pin; Version selects an exact existing resource. Only parameters
// accept Value. Secret material has no representation in a migration.
type ApplicationMigrationChange struct {
	Alias       string
	FromAlias   string
	Key         string
	Value       *string
	ContentType string
	Version     uint64
}
type ApplicationReleaseMigrationInput struct {
	ExpectedSourceVersion            *uint64
	ExpectedSourceActivationRevision *uint64
	Namespace                        NamespaceRef
	SchemaVersion                    uint64
	Contract                         []ApplicationContractField
	Changes                          []ApplicationMigrationChange
	Metadata                         string
	Execute                          bool
	PlanDigest                       string
}
type ApplicationMigrationEnvironment struct {
	Environment   string
	ActiveVersion uint64
	SchemaVersion uint64
}
type ApplicationReleaseMigrationResult struct {
	PlanDigest               string
	Valid                    bool
	Executed                 bool
	DefinitionChanged        bool
	ReleaseName              string
	SourceVersion            uint64
	SourceActivationRevision uint64
	SchemaVersion            uint64
	Entries                  []ApplicationReleasePlanEntry
	Validation               []ReleaseValidationError
	AffectedEnvironments     []ApplicationMigrationEnvironment
	Release                  *ConfigurationRelease
	Activation               *ShipActivation
}
