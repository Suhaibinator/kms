from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class NamespaceRef(_message.Message):
    __slots__ = ("env", "app")
    ENV_FIELD_NUMBER: _ClassVar[int]
    APP_FIELD_NUMBER: _ClassVar[int]
    env: str
    app: str
    def __init__(self, env: _Optional[str] = ..., app: _Optional[str] = ...) -> None: ...

class ResourceRef(_message.Message):
    __slots__ = ("namespace", "key")
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    KEY_FIELD_NUMBER: _ClassVar[int]
    namespace: NamespaceRef
    key: str
    def __init__(self, namespace: _Optional[_Union[NamespaceRef, _Mapping]] = ..., key: _Optional[str] = ...) -> None: ...

class WatchSelector(_message.Message):
    __slots__ = ("namespace", "key_pattern")
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    KEY_PATTERN_FIELD_NUMBER: _ClassVar[int]
    namespace: NamespaceRef
    key_pattern: str
    def __init__(self, namespace: _Optional[_Union[NamespaceRef, _Mapping]] = ..., key_pattern: _Optional[str] = ...) -> None: ...

class Parameter(_message.Message):
    __slots__ = ("ref", "value", "content_type", "version", "metadata_json", "created_by", "created_at_unix_ms", "labels")
    class LabelsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    REF_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LABELS_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    value: str
    content_type: str
    version: int
    metadata_json: str
    created_by: str
    created_at_unix_ms: int
    labels: _containers.ScalarMap[str, int]
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., value: _Optional[str] = ..., content_type: _Optional[str] = ..., version: _Optional[int] = ..., metadata_json: _Optional[str] = ..., created_by: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., labels: _Optional[_Mapping[str, int]] = ...) -> None: ...

class ParameterVersionInfo(_message.Message):
    __slots__ = ("version", "content_type", "state", "created_by", "created_at_unix_ms", "metadata_json")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    version: int
    content_type: str
    state: str
    created_by: str
    created_at_unix_ms: int
    metadata_json: str
    def __init__(self, version: _Optional[int] = ..., content_type: _Optional[str] = ..., state: _Optional[str] = ..., created_by: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., metadata_json: _Optional[str] = ...) -> None: ...

class SecretMetadata(_message.Message):
    __slots__ = ("ref", "content_type", "client_bound", "has_access_token", "metadata_json", "created_at_unix_ms", "updated_at_unix_ms", "labels", "versions")
    class LabelsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    REF_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    CLIENT_BOUND_FIELD_NUMBER: _ClassVar[int]
    HAS_ACCESS_TOKEN_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LABELS_FIELD_NUMBER: _ClassVar[int]
    VERSIONS_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    content_type: str
    client_bound: bool
    has_access_token: bool
    metadata_json: str
    created_at_unix_ms: int
    updated_at_unix_ms: int
    labels: _containers.ScalarMap[str, int]
    versions: _containers.RepeatedCompositeFieldContainer[SecretVersionInfo]
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., content_type: _Optional[str] = ..., client_bound: _Optional[bool] = ..., has_access_token: _Optional[bool] = ..., metadata_json: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., updated_at_unix_ms: _Optional[int] = ..., labels: _Optional[_Mapping[str, int]] = ..., versions: _Optional[_Iterable[_Union[SecretVersionInfo, _Mapping]]] = ...) -> None: ...

class SecretVersionInfo(_message.Message):
    __slots__ = ("version", "state", "created_by", "created_at_unix_ms", "destroyed_at_unix_ms", "expires_at_unix_ms", "metadata_json")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    DESTROYED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    version: int
    state: str
    created_by: str
    created_at_unix_ms: int
    destroyed_at_unix_ms: int
    expires_at_unix_ms: int
    metadata_json: str
    def __init__(self, version: _Optional[int] = ..., state: _Optional[str] = ..., created_by: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., destroyed_at_unix_ms: _Optional[int] = ..., expires_at_unix_ms: _Optional[int] = ..., metadata_json: _Optional[str] = ...) -> None: ...

class GetParameterRequest(_message.Message):
    __slots__ = ("ref", "version", "label")
    REF_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    version: int
    label: str
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., version: _Optional[int] = ..., label: _Optional[str] = ...) -> None: ...

class GetParameterResponse(_message.Message):
    __slots__ = ("parameter",)
    PARAMETER_FIELD_NUMBER: _ClassVar[int]
    parameter: Parameter
    def __init__(self, parameter: _Optional[_Union[Parameter, _Mapping]] = ...) -> None: ...

class PutParameterRequest(_message.Message):
    __slots__ = ("ref", "value", "content_type", "metadata_json")
    REF_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    value: str
    content_type: str
    metadata_json: str
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., value: _Optional[str] = ..., content_type: _Optional[str] = ..., metadata_json: _Optional[str] = ...) -> None: ...

class PutParameterResponse(_message.Message):
    __slots__ = ("version", "revision")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    REVISION_FIELD_NUMBER: _ClassVar[int]
    version: int
    revision: int
    def __init__(self, version: _Optional[int] = ..., revision: _Optional[int] = ...) -> None: ...

class ListParametersRequest(_message.Message):
    __slots__ = ("namespace", "key_prefix", "page_size", "page_token")
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    KEY_PREFIX_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    namespace: NamespaceRef
    key_prefix: str
    page_size: int
    page_token: str
    def __init__(self, namespace: _Optional[_Union[NamespaceRef, _Mapping]] = ..., key_prefix: _Optional[str] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListParametersResponse(_message.Message):
    __slots__ = ("parameters", "next_page_token")
    PARAMETERS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    parameters: _containers.RepeatedCompositeFieldContainer[Parameter]
    next_page_token: str
    def __init__(self, parameters: _Optional[_Iterable[_Union[Parameter, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class DeleteParameterRequest(_message.Message):
    __slots__ = ("ref",)
    REF_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ...) -> None: ...

class DeleteParameterResponse(_message.Message):
    __slots__ = ("revision",)
    REVISION_FIELD_NUMBER: _ClassVar[int]
    revision: int
    def __init__(self, revision: _Optional[int] = ...) -> None: ...

class GetParameterMetadataRequest(_message.Message):
    __slots__ = ("ref",)
    REF_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ...) -> None: ...

class GetParameterMetadataResponse(_message.Message):
    __slots__ = ("ref", "content_type", "metadata_json", "created_at_unix_ms", "updated_at_unix_ms", "labels", "versions")
    class LabelsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    REF_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LABELS_FIELD_NUMBER: _ClassVar[int]
    VERSIONS_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    content_type: str
    metadata_json: str
    created_at_unix_ms: int
    updated_at_unix_ms: int
    labels: _containers.ScalarMap[str, int]
    versions: _containers.RepeatedCompositeFieldContainer[ParameterVersionInfo]
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., content_type: _Optional[str] = ..., metadata_json: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., updated_at_unix_ms: _Optional[int] = ..., labels: _Optional[_Mapping[str, int]] = ..., versions: _Optional[_Iterable[_Union[ParameterVersionInfo, _Mapping]]] = ...) -> None: ...

class GetSecretRequest(_message.Message):
    __slots__ = ("ref", "version", "label")
    REF_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    version: int
    label: str
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., version: _Optional[int] = ..., label: _Optional[str] = ...) -> None: ...

class GetSecretResponse(_message.Message):
    __slots__ = ("ref", "version", "value", "content_type", "metadata_json", "created_at_unix_ms")
    REF_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    version: int
    value: bytes
    content_type: str
    metadata_json: str
    created_at_unix_ms: int
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., version: _Optional[int] = ..., value: _Optional[bytes] = ..., content_type: _Optional[str] = ..., metadata_json: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ...) -> None: ...

class PutSecretRequest(_message.Message):
    __slots__ = ("ref", "value", "content_type", "metadata_json", "client_bound", "generate_access_token", "expires_at_unix_ms")
    REF_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CLIENT_BOUND_FIELD_NUMBER: _ClassVar[int]
    GENERATE_ACCESS_TOKEN_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    value: bytes
    content_type: str
    metadata_json: str
    client_bound: bool
    generate_access_token: bool
    expires_at_unix_ms: int
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., value: _Optional[bytes] = ..., content_type: _Optional[str] = ..., metadata_json: _Optional[str] = ..., client_bound: _Optional[bool] = ..., generate_access_token: _Optional[bool] = ..., expires_at_unix_ms: _Optional[int] = ...) -> None: ...

class PutSecretResponse(_message.Message):
    __slots__ = ("version", "revision", "access_token")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    REVISION_FIELD_NUMBER: _ClassVar[int]
    ACCESS_TOKEN_FIELD_NUMBER: _ClassVar[int]
    version: int
    revision: int
    access_token: str
    def __init__(self, version: _Optional[int] = ..., revision: _Optional[int] = ..., access_token: _Optional[str] = ...) -> None: ...

class ListSecretsRequest(_message.Message):
    __slots__ = ("namespace", "key_prefix", "page_size", "page_token")
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    KEY_PREFIX_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    namespace: NamespaceRef
    key_prefix: str
    page_size: int
    page_token: str
    def __init__(self, namespace: _Optional[_Union[NamespaceRef, _Mapping]] = ..., key_prefix: _Optional[str] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListSecretsResponse(_message.Message):
    __slots__ = ("secrets", "next_page_token")
    SECRETS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    secrets: _containers.RepeatedCompositeFieldContainer[SecretMetadata]
    next_page_token: str
    def __init__(self, secrets: _Optional[_Iterable[_Union[SecretMetadata, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class DeleteSecretRequest(_message.Message):
    __slots__ = ("ref",)
    REF_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ...) -> None: ...

class DeleteSecretResponse(_message.Message):
    __slots__ = ("revision",)
    REVISION_FIELD_NUMBER: _ClassVar[int]
    revision: int
    def __init__(self, revision: _Optional[int] = ...) -> None: ...

class DisableSecretRequest(_message.Message):
    __slots__ = ("ref", "version", "enable")
    REF_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    ENABLE_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    version: int
    enable: bool
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., version: _Optional[int] = ..., enable: _Optional[bool] = ...) -> None: ...

class DisableSecretResponse(_message.Message):
    __slots__ = ("revision",)
    REVISION_FIELD_NUMBER: _ClassVar[int]
    revision: int
    def __init__(self, revision: _Optional[int] = ...) -> None: ...

class DestroySecretVersionRequest(_message.Message):
    __slots__ = ("ref", "version")
    REF_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    version: int
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., version: _Optional[int] = ...) -> None: ...

class DestroySecretVersionResponse(_message.Message):
    __slots__ = ("revision",)
    REVISION_FIELD_NUMBER: _ClassVar[int]
    revision: int
    def __init__(self, revision: _Optional[int] = ...) -> None: ...

class GetSecretMetadataRequest(_message.Message):
    __slots__ = ("ref",)
    REF_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ...) -> None: ...

class GetSecretMetadataResponse(_message.Message):
    __slots__ = ("secret",)
    SECRET_FIELD_NUMBER: _ClassVar[int]
    secret: SecretMetadata
    def __init__(self, secret: _Optional[_Union[SecretMetadata, _Mapping]] = ...) -> None: ...

class PromoteSecretVersionRequest(_message.Message):
    __slots__ = ("ref", "version")
    REF_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    version: int
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., version: _Optional[int] = ...) -> None: ...

class PromoteSecretVersionResponse(_message.Message):
    __slots__ = ("current_version", "previous_version", "revision")
    CURRENT_VERSION_FIELD_NUMBER: _ClassVar[int]
    PREVIOUS_VERSION_FIELD_NUMBER: _ClassVar[int]
    REVISION_FIELD_NUMBER: _ClassVar[int]
    current_version: int
    previous_version: int
    revision: int
    def __init__(self, current_version: _Optional[int] = ..., previous_version: _Optional[int] = ..., revision: _Optional[int] = ...) -> None: ...

class SubscribeRequest(_message.Message):
    __slots__ = ("client_name", "selectors", "last_seen_revision", "acked_revision")
    CLIENT_NAME_FIELD_NUMBER: _ClassVar[int]
    SELECTORS_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_REVISION_FIELD_NUMBER: _ClassVar[int]
    ACKED_REVISION_FIELD_NUMBER: _ClassVar[int]
    client_name: str
    selectors: _containers.RepeatedCompositeFieldContainer[WatchSelector]
    last_seen_revision: int
    acked_revision: int
    def __init__(self, client_name: _Optional[str] = ..., selectors: _Optional[_Iterable[_Union[WatchSelector, _Mapping]]] = ..., last_seen_revision: _Optional[int] = ..., acked_revision: _Optional[int] = ...) -> None: ...

class SubscribeEvent(_message.Message):
    __slots__ = ("snapshot", "change", "secret_change", "heartbeat", "revision")
    SNAPSHOT_FIELD_NUMBER: _ClassVar[int]
    CHANGE_FIELD_NUMBER: _ClassVar[int]
    SECRET_CHANGE_FIELD_NUMBER: _ClassVar[int]
    HEARTBEAT_FIELD_NUMBER: _ClassVar[int]
    REVISION_FIELD_NUMBER: _ClassVar[int]
    snapshot: Snapshot
    change: ParameterChange
    secret_change: SecretMetadataChange
    heartbeat: Heartbeat
    revision: int
    def __init__(self, snapshot: _Optional[_Union[Snapshot, _Mapping]] = ..., change: _Optional[_Union[ParameterChange, _Mapping]] = ..., secret_change: _Optional[_Union[SecretMetadataChange, _Mapping]] = ..., heartbeat: _Optional[_Union[Heartbeat, _Mapping]] = ..., revision: _Optional[int] = ...) -> None: ...

class Snapshot(_message.Message):
    __slots__ = ("parameters",)
    PARAMETERS_FIELD_NUMBER: _ClassVar[int]
    parameters: _containers.RepeatedCompositeFieldContainer[Parameter]
    def __init__(self, parameters: _Optional[_Iterable[_Union[Parameter, _Mapping]]] = ...) -> None: ...

class ParameterChange(_message.Message):
    __slots__ = ("ref", "change_type", "value", "content_type", "version", "label")
    REF_FIELD_NUMBER: _ClassVar[int]
    CHANGE_TYPE_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    change_type: str
    value: str
    content_type: str
    version: int
    label: str
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., change_type: _Optional[str] = ..., value: _Optional[str] = ..., content_type: _Optional[str] = ..., version: _Optional[int] = ..., label: _Optional[str] = ...) -> None: ...

class SecretMetadataChange(_message.Message):
    __slots__ = ("ref", "change_type", "version", "label")
    REF_FIELD_NUMBER: _ClassVar[int]
    CHANGE_TYPE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    change_type: str
    version: int
    label: str
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., change_type: _Optional[str] = ..., version: _Optional[int] = ..., label: _Optional[str] = ...) -> None: ...

class Heartbeat(_message.Message):
    __slots__ = ("server_time_unix_ms",)
    SERVER_TIME_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    server_time_unix_ms: int
    def __init__(self, server_time_unix_ms: _Optional[int] = ...) -> None: ...

class WatchParameterRequest(_message.Message):
    __slots__ = ("ref", "last_seen_revision")
    REF_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_REVISION_FIELD_NUMBER: _ClassVar[int]
    ref: ResourceRef
    last_seen_revision: int
    def __init__(self, ref: _Optional[_Union[ResourceRef, _Mapping]] = ..., last_seen_revision: _Optional[int] = ...) -> None: ...

class WatchNamespaceRequest(_message.Message):
    __slots__ = ("namespace", "key_pattern", "last_seen_revision")
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    KEY_PATTERN_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_REVISION_FIELD_NUMBER: _ClassVar[int]
    namespace: NamespaceRef
    key_pattern: str
    last_seen_revision: int
    def __init__(self, namespace: _Optional[_Union[NamespaceRef, _Mapping]] = ..., key_pattern: _Optional[str] = ..., last_seen_revision: _Optional[int] = ...) -> None: ...

class Namespace(_message.Message):
    __slots__ = ("ref", "description", "allowed_auth_methods", "created_by", "created_at_unix_ms", "parameter_count", "secret_count")
    REF_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    ALLOWED_AUTH_METHODS_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    PARAMETER_COUNT_FIELD_NUMBER: _ClassVar[int]
    SECRET_COUNT_FIELD_NUMBER: _ClassVar[int]
    ref: NamespaceRef
    description: str
    allowed_auth_methods: _containers.RepeatedScalarFieldContainer[str]
    created_by: str
    created_at_unix_ms: int
    parameter_count: int
    secret_count: int
    def __init__(self, ref: _Optional[_Union[NamespaceRef, _Mapping]] = ..., description: _Optional[str] = ..., allowed_auth_methods: _Optional[_Iterable[str]] = ..., created_by: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., parameter_count: _Optional[int] = ..., secret_count: _Optional[int] = ...) -> None: ...

class CertBundle(_message.Message):
    __slots__ = ("cert_pem", "key_pem", "serial", "not_after_unix_ms")
    CERT_PEM_FIELD_NUMBER: _ClassVar[int]
    KEY_PEM_FIELD_NUMBER: _ClassVar[int]
    SERIAL_FIELD_NUMBER: _ClassVar[int]
    NOT_AFTER_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    cert_pem: str
    key_pem: str
    serial: str
    not_after_unix_ms: int
    def __init__(self, cert_pem: _Optional[str] = ..., key_pem: _Optional[str] = ..., serial: _Optional[str] = ..., not_after_unix_ms: _Optional[int] = ...) -> None: ...

class IdentityCertInfo(_message.Message):
    __slots__ = ("serial", "fingerprint", "not_after_unix_ms", "revoked_at_unix_ms", "created_at_unix_ms")
    SERIAL_FIELD_NUMBER: _ClassVar[int]
    FINGERPRINT_FIELD_NUMBER: _ClassVar[int]
    NOT_AFTER_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    REVOKED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    serial: str
    fingerprint: str
    not_after_unix_ms: int
    revoked_at_unix_ms: int
    created_at_unix_ms: int
    def __init__(self, serial: _Optional[str] = ..., fingerprint: _Optional[str] = ..., not_after_unix_ms: _Optional[int] = ..., revoked_at_unix_ms: _Optional[int] = ..., created_at_unix_ms: _Optional[int] = ...) -> None: ...

class Identity(_message.Message):
    __slots__ = ("name", "kind", "disabled", "created_at_unix_ms", "namespace", "has_token", "certs")
    NAME_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    DISABLED_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    HAS_TOKEN_FIELD_NUMBER: _ClassVar[int]
    CERTS_FIELD_NUMBER: _ClassVar[int]
    name: str
    kind: str
    disabled: bool
    created_at_unix_ms: int
    namespace: NamespaceRef
    has_token: bool
    certs: _containers.RepeatedCompositeFieldContainer[IdentityCertInfo]
    def __init__(self, name: _Optional[str] = ..., kind: _Optional[str] = ..., disabled: _Optional[bool] = ..., created_at_unix_ms: _Optional[int] = ..., namespace: _Optional[_Union[NamespaceRef, _Mapping]] = ..., has_token: _Optional[bool] = ..., certs: _Optional[_Iterable[_Union[IdentityCertInfo, _Mapping]]] = ...) -> None: ...

class CreateNamespaceRequest(_message.Message):
    __slots__ = ("ref", "description", "allowed_auth_methods")
    REF_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    ALLOWED_AUTH_METHODS_FIELD_NUMBER: _ClassVar[int]
    ref: NamespaceRef
    description: str
    allowed_auth_methods: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, ref: _Optional[_Union[NamespaceRef, _Mapping]] = ..., description: _Optional[str] = ..., allowed_auth_methods: _Optional[_Iterable[str]] = ...) -> None: ...

class CreateNamespaceResponse(_message.Message):
    __slots__ = ("namespace",)
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    namespace: Namespace
    def __init__(self, namespace: _Optional[_Union[Namespace, _Mapping]] = ...) -> None: ...

class UpdateNamespaceRequest(_message.Message):
    __slots__ = ("ref", "description", "allowed_auth_methods")
    REF_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    ALLOWED_AUTH_METHODS_FIELD_NUMBER: _ClassVar[int]
    ref: NamespaceRef
    description: str
    allowed_auth_methods: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, ref: _Optional[_Union[NamespaceRef, _Mapping]] = ..., description: _Optional[str] = ..., allowed_auth_methods: _Optional[_Iterable[str]] = ...) -> None: ...

class UpdateNamespaceResponse(_message.Message):
    __slots__ = ("namespace",)
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    namespace: Namespace
    def __init__(self, namespace: _Optional[_Union[Namespace, _Mapping]] = ...) -> None: ...

class DeleteNamespaceRequest(_message.Message):
    __slots__ = ("ref",)
    REF_FIELD_NUMBER: _ClassVar[int]
    ref: NamespaceRef
    def __init__(self, ref: _Optional[_Union[NamespaceRef, _Mapping]] = ...) -> None: ...

class DeleteNamespaceResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ListNamespacesRequest(_message.Message):
    __slots__ = ("page_size", "page_token")
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    page_size: int
    page_token: str
    def __init__(self, page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListNamespacesResponse(_message.Message):
    __slots__ = ("namespaces", "next_page_token")
    NAMESPACES_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    namespaces: _containers.RepeatedCompositeFieldContainer[Namespace]
    next_page_token: str
    def __init__(self, namespaces: _Optional[_Iterable[_Union[Namespace, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class PolicyRule(_message.Message):
    __slots__ = ("operation", "env", "app", "key")
    OPERATION_FIELD_NUMBER: _ClassVar[int]
    ENV_FIELD_NUMBER: _ClassVar[int]
    APP_FIELD_NUMBER: _ClassVar[int]
    KEY_FIELD_NUMBER: _ClassVar[int]
    operation: str
    env: str
    app: str
    key: str
    def __init__(self, operation: _Optional[str] = ..., env: _Optional[str] = ..., app: _Optional[str] = ..., key: _Optional[str] = ...) -> None: ...

class Policy(_message.Message):
    __slots__ = ("name", "subject", "allow", "deny", "created_at_unix_ms", "updated_at_unix_ms")
    NAME_FIELD_NUMBER: _ClassVar[int]
    SUBJECT_FIELD_NUMBER: _ClassVar[int]
    ALLOW_FIELD_NUMBER: _ClassVar[int]
    DENY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    name: str
    subject: str
    allow: _containers.RepeatedCompositeFieldContainer[PolicyRule]
    deny: _containers.RepeatedCompositeFieldContainer[PolicyRule]
    created_at_unix_ms: int
    updated_at_unix_ms: int
    def __init__(self, name: _Optional[str] = ..., subject: _Optional[str] = ..., allow: _Optional[_Iterable[_Union[PolicyRule, _Mapping]]] = ..., deny: _Optional[_Iterable[_Union[PolicyRule, _Mapping]]] = ..., created_at_unix_ms: _Optional[int] = ..., updated_at_unix_ms: _Optional[int] = ...) -> None: ...

class CreatePolicyRequest(_message.Message):
    __slots__ = ("policy",)
    POLICY_FIELD_NUMBER: _ClassVar[int]
    policy: Policy
    def __init__(self, policy: _Optional[_Union[Policy, _Mapping]] = ...) -> None: ...

class CreatePolicyResponse(_message.Message):
    __slots__ = ("policy",)
    POLICY_FIELD_NUMBER: _ClassVar[int]
    policy: Policy
    def __init__(self, policy: _Optional[_Union[Policy, _Mapping]] = ...) -> None: ...

class UpdatePolicyRequest(_message.Message):
    __slots__ = ("policy",)
    POLICY_FIELD_NUMBER: _ClassVar[int]
    policy: Policy
    def __init__(self, policy: _Optional[_Union[Policy, _Mapping]] = ...) -> None: ...

class UpdatePolicyResponse(_message.Message):
    __slots__ = ("policy",)
    POLICY_FIELD_NUMBER: _ClassVar[int]
    policy: Policy
    def __init__(self, policy: _Optional[_Union[Policy, _Mapping]] = ...) -> None: ...

class DeletePolicyRequest(_message.Message):
    __slots__ = ("name",)
    NAME_FIELD_NUMBER: _ClassVar[int]
    name: str
    def __init__(self, name: _Optional[str] = ...) -> None: ...

class DeletePolicyResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ListPoliciesRequest(_message.Message):
    __slots__ = ("page_size", "page_token")
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    page_size: int
    page_token: str
    def __init__(self, page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListPoliciesResponse(_message.Message):
    __slots__ = ("policies", "next_page_token")
    POLICIES_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    policies: _containers.RepeatedCompositeFieldContainer[Policy]
    next_page_token: str
    def __init__(self, policies: _Optional[_Iterable[_Union[Policy, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class CreateIdentityRequest(_message.Message):
    __slots__ = ("name", "kind", "namespace", "auth_methods", "cert_ttl_seconds")
    NAME_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    AUTH_METHODS_FIELD_NUMBER: _ClassVar[int]
    CERT_TTL_SECONDS_FIELD_NUMBER: _ClassVar[int]
    name: str
    kind: str
    namespace: NamespaceRef
    auth_methods: _containers.RepeatedScalarFieldContainer[str]
    cert_ttl_seconds: int
    def __init__(self, name: _Optional[str] = ..., kind: _Optional[str] = ..., namespace: _Optional[_Union[NamespaceRef, _Mapping]] = ..., auth_methods: _Optional[_Iterable[str]] = ..., cert_ttl_seconds: _Optional[int] = ...) -> None: ...

class CreateIdentityResponse(_message.Message):
    __slots__ = ("identity", "token", "cert")
    IDENTITY_FIELD_NUMBER: _ClassVar[int]
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    CERT_FIELD_NUMBER: _ClassVar[int]
    identity: Identity
    token: str
    cert: CertBundle
    def __init__(self, identity: _Optional[_Union[Identity, _Mapping]] = ..., token: _Optional[str] = ..., cert: _Optional[_Union[CertBundle, _Mapping]] = ...) -> None: ...

class ListIdentitiesRequest(_message.Message):
    __slots__ = ("page_size", "page_token")
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    page_size: int
    page_token: str
    def __init__(self, page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListIdentitiesResponse(_message.Message):
    __slots__ = ("identities", "next_page_token")
    IDENTITIES_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    identities: _containers.RepeatedCompositeFieldContainer[Identity]
    next_page_token: str
    def __init__(self, identities: _Optional[_Iterable[_Union[Identity, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class RevokeIdentityRequest(_message.Message):
    __slots__ = ("name",)
    NAME_FIELD_NUMBER: _ClassVar[int]
    name: str
    def __init__(self, name: _Optional[str] = ...) -> None: ...

class RevokeIdentityResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class RotateIdentityTokenRequest(_message.Message):
    __slots__ = ("name",)
    NAME_FIELD_NUMBER: _ClassVar[int]
    name: str
    def __init__(self, name: _Optional[str] = ...) -> None: ...

class RotateIdentityTokenResponse(_message.Message):
    __slots__ = ("token",)
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    token: str
    def __init__(self, token: _Optional[str] = ...) -> None: ...

class IssueIdentityCertificateRequest(_message.Message):
    __slots__ = ("name", "ttl_seconds")
    NAME_FIELD_NUMBER: _ClassVar[int]
    TTL_SECONDS_FIELD_NUMBER: _ClassVar[int]
    name: str
    ttl_seconds: int
    def __init__(self, name: _Optional[str] = ..., ttl_seconds: _Optional[int] = ...) -> None: ...

class IssueIdentityCertificateResponse(_message.Message):
    __slots__ = ("cert",)
    CERT_FIELD_NUMBER: _ClassVar[int]
    cert: CertBundle
    def __init__(self, cert: _Optional[_Union[CertBundle, _Mapping]] = ...) -> None: ...

class RevokeIdentityCertificateRequest(_message.Message):
    __slots__ = ("name", "serial")
    NAME_FIELD_NUMBER: _ClassVar[int]
    SERIAL_FIELD_NUMBER: _ClassVar[int]
    name: str
    serial: str
    def __init__(self, name: _Optional[str] = ..., serial: _Optional[str] = ...) -> None: ...

class RevokeIdentityCertificateResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class WhoAmIRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class WhoAmIResponse(_message.Message):
    __slots__ = ("name", "kind", "namespace", "auth_method")
    NAME_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    AUTH_METHOD_FIELD_NUMBER: _ClassVar[int]
    name: str
    kind: str
    namespace: NamespaceRef
    auth_method: str
    def __init__(self, name: _Optional[str] = ..., kind: _Optional[str] = ..., namespace: _Optional[_Union[NamespaceRef, _Mapping]] = ..., auth_method: _Optional[str] = ...) -> None: ...

class GetCACertificateRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class GetCACertificateResponse(_message.Message):
    __slots__ = ("cert_pem",)
    CERT_PEM_FIELD_NUMBER: _ClassVar[int]
    cert_pem: str
    def __init__(self, cert_pem: _Optional[str] = ...) -> None: ...

class AuditEvent(_message.Message):
    __slots__ = ("id", "event_type", "actor_identity", "actor_type", "resource_type", "resource_env", "resource_app", "resource_key", "resource_version", "decision", "source_ip", "user_agent", "request_id", "created_at_unix_ms", "metadata_json")
    ID_FIELD_NUMBER: _ClassVar[int]
    EVENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    ACTOR_IDENTITY_FIELD_NUMBER: _ClassVar[int]
    ACTOR_TYPE_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_TYPE_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_ENV_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_APP_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_KEY_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_VERSION_FIELD_NUMBER: _ClassVar[int]
    DECISION_FIELD_NUMBER: _ClassVar[int]
    SOURCE_IP_FIELD_NUMBER: _ClassVar[int]
    USER_AGENT_FIELD_NUMBER: _ClassVar[int]
    REQUEST_ID_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    id: int
    event_type: str
    actor_identity: str
    actor_type: str
    resource_type: str
    resource_env: str
    resource_app: str
    resource_key: str
    resource_version: int
    decision: str
    source_ip: str
    user_agent: str
    request_id: str
    created_at_unix_ms: int
    metadata_json: str
    def __init__(self, id: _Optional[int] = ..., event_type: _Optional[str] = ..., actor_identity: _Optional[str] = ..., actor_type: _Optional[str] = ..., resource_type: _Optional[str] = ..., resource_env: _Optional[str] = ..., resource_app: _Optional[str] = ..., resource_key: _Optional[str] = ..., resource_version: _Optional[int] = ..., decision: _Optional[str] = ..., source_ip: _Optional[str] = ..., user_agent: _Optional[str] = ..., request_id: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., metadata_json: _Optional[str] = ...) -> None: ...

class ListAuditEventsRequest(_message.Message):
    __slots__ = ("env", "app", "key_prefix", "actor_identity", "event_type", "from_unix_ms", "to_unix_ms", "page_size", "page_token")
    ENV_FIELD_NUMBER: _ClassVar[int]
    APP_FIELD_NUMBER: _ClassVar[int]
    KEY_PREFIX_FIELD_NUMBER: _ClassVar[int]
    ACTOR_IDENTITY_FIELD_NUMBER: _ClassVar[int]
    EVENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    FROM_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    TO_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    env: str
    app: str
    key_prefix: str
    actor_identity: str
    event_type: str
    from_unix_ms: int
    to_unix_ms: int
    page_size: int
    page_token: str
    def __init__(self, env: _Optional[str] = ..., app: _Optional[str] = ..., key_prefix: _Optional[str] = ..., actor_identity: _Optional[str] = ..., event_type: _Optional[str] = ..., from_unix_ms: _Optional[int] = ..., to_unix_ms: _Optional[int] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListAuditEventsResponse(_message.Message):
    __slots__ = ("events", "next_page_token")
    EVENTS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    events: _containers.RepeatedCompositeFieldContainer[AuditEvent]
    next_page_token: str
    def __init__(self, events: _Optional[_Iterable[_Union[AuditEvent, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class Subscriber(_message.Message):
    __slots__ = ("client_name", "instance_id", "identity", "selectors", "remote_addr", "connected_at_unix_ms", "last_heartbeat_unix_ms", "last_acked_revision")
    CLIENT_NAME_FIELD_NUMBER: _ClassVar[int]
    INSTANCE_ID_FIELD_NUMBER: _ClassVar[int]
    IDENTITY_FIELD_NUMBER: _ClassVar[int]
    SELECTORS_FIELD_NUMBER: _ClassVar[int]
    REMOTE_ADDR_FIELD_NUMBER: _ClassVar[int]
    CONNECTED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LAST_HEARTBEAT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LAST_ACKED_REVISION_FIELD_NUMBER: _ClassVar[int]
    client_name: str
    instance_id: str
    identity: str
    selectors: _containers.RepeatedCompositeFieldContainer[WatchSelector]
    remote_addr: str
    connected_at_unix_ms: int
    last_heartbeat_unix_ms: int
    last_acked_revision: int
    def __init__(self, client_name: _Optional[str] = ..., instance_id: _Optional[str] = ..., identity: _Optional[str] = ..., selectors: _Optional[_Iterable[_Union[WatchSelector, _Mapping]]] = ..., remote_addr: _Optional[str] = ..., connected_at_unix_ms: _Optional[int] = ..., last_heartbeat_unix_ms: _Optional[int] = ..., last_acked_revision: _Optional[int] = ...) -> None: ...

class ListSubscribersRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ListSubscribersResponse(_message.Message):
    __slots__ = ("subscribers", "current_revision")
    SUBSCRIBERS_FIELD_NUMBER: _ClassVar[int]
    CURRENT_REVISION_FIELD_NUMBER: _ClassVar[int]
    subscribers: _containers.RepeatedCompositeFieldContainer[Subscriber]
    current_revision: int
    def __init__(self, subscribers: _Optional[_Iterable[_Union[Subscriber, _Mapping]]] = ..., current_revision: _Optional[int] = ...) -> None: ...

class HealthRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class HealthResponse(_message.Message):
    __slots__ = ("healthy", "ready", "version", "current_revision", "details_json")
    HEALTHY_FIELD_NUMBER: _ClassVar[int]
    READY_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    CURRENT_REVISION_FIELD_NUMBER: _ClassVar[int]
    DETAILS_JSON_FIELD_NUMBER: _ClassVar[int]
    healthy: bool
    ready: bool
    version: str
    current_revision: int
    details_json: str
    def __init__(self, healthy: _Optional[bool] = ..., ready: _Optional[bool] = ..., version: _Optional[str] = ..., current_revision: _Optional[int] = ..., details_json: _Optional[str] = ...) -> None: ...
