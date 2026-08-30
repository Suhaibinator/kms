"""Managed Pydantic configuration with Go/TypeScript-compatible semantics."""

from .canonical import canonical_parameter_value, parameter_hash
from .contract import ContractEntry, validate_contract, validate_manifest
from .defaults import (
    DEFAULTS_ARTIFACT_FORMAT,
    DefaultsArtifact,
    DefaultsArtifactError,
    DefaultsParameter,
    encode_defaults_artifact,
    export_defaults,
    parse_defaults_artifact,
)
from .model import ConfigSpec, Inline, Parameter, SecretField, Unmanaged
from .runtime import (
    AsyncManagedConfigManager,
    Callbacks,
    ConfigBinding,
    ConfigView,
    ManagedConfigManager,
    logging_callbacks,
    start_async_managed_config,
    start_managed_config,
)
from .types import (
    AppliedReport,
    CandidateError,
    CandidateRejectionReport,
    ConfigSnapshot,
    DefaultMismatchReport,
    FieldChange,
    FieldDifference,
    ManagedConfigStats,
    ManagedConfigStatus,
    ReleaseIdentity,
)
from .verify import VerifyEntryResult, VerifyResult, verify_defaults, verify_defaults_async

__all__ = [name for name in globals() if not name.startswith("_")]
