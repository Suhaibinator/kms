export {
  createClient,
  KmsClient,
} from "./client.js";
export type {
  CallOptions,
  ClientReleaseLoaderOptions,
  GetOptions,
  KmsClientOptions,
  ListOptions,
  Logger,
  ParameterMetadata,
  PutParameterOptions,
  PutSecretOptions,
  WatchCallback,
  WatchEvent,
  WatchOptions,
} from "./client.js";

export {
  ConfigError,
  isKmsError,
  KmsError,
  mapGrpcError,
  NoNamespaceError,
  normalizeError,
  NotInitializedError,
  wrapError,
} from "./errors.js";
export type { KmsErrorCode, KmsErrorOptions } from "./errors.js";

export type {
  Page,
  Parameter,
  PutResult,
  PutSecretResult,
  SecretInfo,
  SecretVersion,
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
  ReleaseEntryKind,
  ReleaseEntryMetadataInit,
  ReleaseLoaderStats,
  ReleaseLoaderStatus,
  ReleaseManifestInit,
  ReleaseRejectionCategory,
  ReleaseSnapshotInit,
  ReleaseState,
} from "./releases/types.js";

export { newSecret, REDACTED, Secret } from "./secret.js";
export type { SecretMetadata } from "./secret.js";

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
