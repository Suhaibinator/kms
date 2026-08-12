export { cloneConfig, containsSecret } from "./clone.js";
export {
  type BigIntCodecOptions,
  ConfigDecodeError,
  codecs,
  decodeGroup,
  encodeGroup,
  type FieldCodec,
  type FloatCodecOptions,
  field,
  type GroupCodec,
  group,
  type IntegerCodecOptions,
  type ValueCodec,
} from "./codecs.js";
export {
  type ContractEntry,
  type ContractKind,
  createManifestValidator,
  validateContract,
} from "./contract.js";
export {
  CandidateError,
  CandidateRejectionReport,
  DefaultMismatchError,
  DefaultMismatchReport,
  type FieldDifference,
  type MismatchPhase,
  type MismatchSeverity,
  REJECTION_CATEGORIES,
  type RejectionCategory,
  reject,
  rejectDecode,
} from "./errors.js";
export {
  ManagedConfigManager,
  type ManagedConfigOptions,
  type ManagedConfigStats,
  type ManagedConfigStatus,
  type ManagedPreparedCandidate,
  type PrepareManagedCandidate,
  startManagedConfig,
} from "./manager.js";
export {
  ConfigSnapshot,
  immutableSnapshot,
  ReleaseIdentity,
  type ReleaseIdentityInit,
} from "./snapshot.js";
export {
  InvalidJsonDocumentError,
  type JsonNode,
  type JsonProperty,
  parseStrictJson,
  stringifyJsonNode,
} from "./strict-json.js";
