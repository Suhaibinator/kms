export {
  createClient,
  KmsClient,
} from "./client.js";
export type {
  CallOptions,
  BindSecretOptions,
  ClientReleaseLoaderOptions,
  GetOptions,
  KmsClientOptions,
  ListOptions,
  Logger,
  ParameterMetadata,
  PreviewSecretBindingCohortOptions,
  PutParameterOptions,
  PutSecretOptions,
  PurgeSecretBindingCohortOptions,
  RotateSecretBindingKeyOptions,
  SecretBindingCohortGuardOptions,
  WatchCallback,
  WatchEvent,
  WatchOptions,
  WatchStatus,
} from "./client.js";
export type { ReconciliationHealth, WatchConnectionState } from "./watch.js";

export {
  ConfigError,
  isKmsError,
  KmsError,
  mapGrpcError,
  NoNamespaceError,
  normalizeError,
  NotInitializedError,
  RateLimitedError,
  wrapError,
} from "./errors.js";
export type { KmsErrorCode, KmsErrorOptions } from "./errors.js";

export type {
  Page,
  Parameter,
  PutResult,
  PutSecretResult,
  SecretInfo,
  SecretBindingCohortResult,
  SecretVersion,
  SecretVersionMutationResult,
  WhoAmI,
} from "./models.js";

export {
  createPolicyPublisher,
  definePublicProjection,
  formatPublicConfigEtag,
  formatRevision,
  freezePublicJson,
  normalizePublicConfigWire,
  parseRevision,
} from "./publishing.js";
export type {
  AuthoritativeValidator,
  CreatePolicyPublisherOptions,
  DecimalRevision,
  PolicyPublisher,
  PolicyPublisherEvent,
  PolicyPublisherObserver,
  PolicySnapshot,
  PolicyValidationResult,
  PublicConfig,
  PublicConfigWire,
  PublicFieldSelector,
  PublicJsonObject,
  PublicJsonPrimitive,
  PublicJsonValue,
  PublicProjection,
  PublicProjectionMap,
  SnapshotReader,
  ValidationDecision,
  ValidationFailure,
  ValidationSuccess,
} from "./publishing.js";

export {
  CURRENT_VERSION,
  displayNamespace,
  displayPath,
  namespaceEquals,
  namespaceKey,
  normalizeVersionRef,
  parseNamespace,
  refOf,
  resolveRef,
  splitDisplayPath,
  UINT64_MAX,
} from "./refs.js";
export type { NamespaceRef, ResourceRef, VersionRef } from "./refs.js";

export { ReleaseLoader, runTypedRelease } from "./releases/loader.js";
export type {
  SecretTokenProvider,
  ValidateReleaseManifest,
} from "./releases/loader.js";
export {
  classifiedReleaseCategory,
  ClassifiedReleaseError,
  RELEASE_REJECTION_CATEGORIES,
  RELEASE_STATES,
  ReleaseCandidateError,
  ReleaseEntryMetadata,
  ReleaseManifest,
  ReleaseParameter,
  ReleaseSecret,
  ReleaseSnapshot,
} from "./releases/types.js";
export type {
  PreparedRelease,
  PrepareRelease,
  ReleaseDivergence,
  ReleaseEntryKind,
  ReleaseEntryMetadataInit,
  ReleaseLoaderStats,
  ReleaseLoaderStatus,
  ReleaseManifestInit,
  ReleaseRejectionCategory,
  ReleaseSnapshotInit,
  ReleaseState,
} from "./releases/types.js";
export { VERIFY_VERDICTS } from "./releases/verify.js";
export type {
  VerifyDefaultsEntry,
  VerifyDefaultsVerdict,
  VerifyReleaseDefaultsOptions,
  VerifyReleaseDefaultsResult,
  VerifyVerdict,
} from "./releases/verify.js";

export { newSecret, REDACTED, Secret } from "./secret.js";
export type { SecretOptions } from "./secret.js";

export { mtlsFromFiles, tlsFromBytes, tlsFromFiles } from "./tls.js";

export type {
  BidiMethod,
  DuplexRpc,
  RpcTransport,
  TransportCallOptions,
  UnaryMethod,
} from "./transport.js";

export {
  collectDeclarativeValues,
  ParameterValue,
  ResolutionError,
  resolveValues,
  SecretValue,
} from "./values.js";
export type {
  ChangeCallback,
  DeclarativeValue,
  ParameterValueOptions,
  SecretReadOptions,
  SecretValueOptions,
  SubscriptionHandle,
  ValueReadOptions,
  ValueResolver,
} from "./values.js";
