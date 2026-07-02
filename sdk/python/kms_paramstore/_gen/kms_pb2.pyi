from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Parameter(_message.Message):
    __slots__ = ("path", "value", "content_type", "version", "metadata_json", "created_by", "created_at_unix_ms", "labels")
    class LabelsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    PATH_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LABELS_FIELD_NUMBER: _ClassVar[int]
    path: str
    value: str
    content_type: str
    version: int
    metadata_json: str
    created_by: str
    created_at_unix_ms: int
    labels: _containers.ScalarMap[str, int]
    def __init__(self, path: _Optional[str] = ..., value: _Optional[str] = ..., content_type: _Optional[str] = ..., version: _Optional[int] = ..., metadata_json: _Optional[str] = ..., created_by: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., labels: _Optional[_Mapping[str, int]] = ...) -> None: ...

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
    __slots__ = ("path", "content_type", "client_bound", "has_access_token", "metadata_json", "created_at_unix_ms", "updated_at_unix_ms", "labels", "versions")
    class LabelsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    PATH_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    CLIENT_BOUND_FIELD_NUMBER: _ClassVar[int]
    HAS_ACCESS_TOKEN_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LABELS_FIELD_NUMBER: _ClassVar[int]
    VERSIONS_FIELD_NUMBER: _ClassVar[int]
    path: str
    content_type: str
    client_bound: bool
    has_access_token: bool
    metadata_json: str
    created_at_unix_ms: int
    updated_at_unix_ms: int
    labels: _containers.ScalarMap[str, int]
    versions: _containers.RepeatedCompositeFieldContainer[SecretVersionInfo]
    def __init__(self, path: _Optional[str] = ..., content_type: _Optional[str] = ..., client_bound: _Optional[bool] = ..., has_access_token: _Optional[bool] = ..., metadata_json: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., updated_at_unix_ms: _Optional[int] = ..., labels: _Optional[_Mapping[str, int]] = ..., versions: _Optional[_Iterable[_Union[SecretVersionInfo, _Mapping]]] = ...) -> None: ...

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
    __slots__ = ("path", "version", "label")
    PATH_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    path: str
    version: int
    label: str
    def __init__(self, path: _Optional[str] = ..., version: _Optional[int] = ..., label: _Optional[str] = ...) -> None: ...

class GetParameterResponse(_message.Message):
    __slots__ = ("parameter",)
    PARAMETER_FIELD_NUMBER: _ClassVar[int]
    parameter: Parameter
    def __init__(self, parameter: _Optional[_Union[Parameter, _Mapping]] = ...) -> None: ...

class PutParameterRequest(_message.Message):
    __slots__ = ("path", "value", "content_type", "metadata_json")
    PATH_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    path: str
    value: str
    content_type: str
    metadata_json: str
    def __init__(self, path: _Optional[str] = ..., value: _Optional[str] = ..., content_type: _Optional[str] = ..., metadata_json: _Optional[str] = ...) -> None: ...

class PutParameterResponse(_message.Message):
    __slots__ = ("version", "revision")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    REVISION_FIELD_NUMBER: _ClassVar[int]
    version: int
    revision: int
    def __init__(self, version: _Optional[int] = ..., revision: _Optional[int] = ...) -> None: ...

class ListParametersRequest(_message.Message):
    __slots__ = ("path_prefix", "page_size", "page_token")
    PATH_PREFIX_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    path_prefix: str
    page_size: int
    page_token: str
    def __init__(self, path_prefix: _Optional[str] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListParametersResponse(_message.Message):
    __slots__ = ("parameters", "next_page_token")
    PARAMETERS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    parameters: _containers.RepeatedCompositeFieldContainer[Parameter]
    next_page_token: str
    def __init__(self, parameters: _Optional[_Iterable[_Union[Parameter, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class DeleteParameterRequest(_message.Message):
    __slots__ = ("path",)
    PATH_FIELD_NUMBER: _ClassVar[int]
    path: str
    def __init__(self, path: _Optional[str] = ...) -> None: ...

class DeleteParameterResponse(_message.Message):
    __slots__ = ("revision",)
    REVISION_FIELD_NUMBER: _ClassVar[int]
    revision: int
    def __init__(self, revision: _Optional[int] = ...) -> None: ...

class GetParameterMetadataRequest(_message.Message):
    __slots__ = ("path",)
    PATH_FIELD_NUMBER: _ClassVar[int]
    path: str
    def __init__(self, path: _Optional[str] = ...) -> None: ...

class GetParameterMetadataResponse(_message.Message):
    __slots__ = ("path", "content_type", "metadata_json", "created_at_unix_ms", "updated_at_unix_ms", "labels", "versions")
    class LabelsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    PATH_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LABELS_FIELD_NUMBER: _ClassVar[int]
    VERSIONS_FIELD_NUMBER: _ClassVar[int]
    path: str
    content_type: str
    metadata_json: str
    created_at_unix_ms: int
    updated_at_unix_ms: int
    labels: _containers.ScalarMap[str, int]
    versions: _containers.RepeatedCompositeFieldContainer[ParameterVersionInfo]
    def __init__(self, path: _Optional[str] = ..., content_type: _Optional[str] = ..., metadata_json: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., updated_at_unix_ms: _Optional[int] = ..., labels: _Optional[_Mapping[str, int]] = ..., versions: _Optional[_Iterable[_Union[ParameterVersionInfo, _Mapping]]] = ...) -> None: ...

class GetSecretRequest(_message.Message):
    __slots__ = ("path", "version", "label")
    PATH_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    path: str
    version: int
    label: str
    def __init__(self, path: _Optional[str] = ..., version: _Optional[int] = ..., label: _Optional[str] = ...) -> None: ...

class GetSecretResponse(_message.Message):
    __slots__ = ("path", "version", "value", "content_type", "metadata_json", "created_at_unix_ms")
    PATH_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    path: str
    version: int
    value: bytes
    content_type: str
    metadata_json: str
    created_at_unix_ms: int
    def __init__(self, path: _Optional[str] = ..., version: _Optional[int] = ..., value: _Optional[bytes] = ..., content_type: _Optional[str] = ..., metadata_json: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ...) -> None: ...

class PutSecretRequest(_message.Message):
    __slots__ = ("path", "value", "content_type", "metadata_json", "client_bound", "generate_access_token", "expires_at_unix_ms")
    PATH_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    METADATA_JSON_FIELD_NUMBER: _ClassVar[int]
    CLIENT_BOUND_FIELD_NUMBER: _ClassVar[int]
    GENERATE_ACCESS_TOKEN_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    path: str
    value: bytes
    content_type: str
    metadata_json: str
    client_bound: bool
    generate_access_token: bool
    expires_at_unix_ms: int
    def __init__(self, path: _Optional[str] = ..., value: _Optional[bytes] = ..., content_type: _Optional[str] = ..., metadata_json: _Optional[str] = ..., client_bound: _Optional[bool] = ..., generate_access_token: _Optional[bool] = ..., expires_at_unix_ms: _Optional[int] = ...) -> None: ...

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
    __slots__ = ("path_prefix", "page_size", "page_token")
    PATH_PREFIX_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    path_prefix: str
    page_size: int
    page_token: str
    def __init__(self, path_prefix: _Optional[str] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListSecretsResponse(_message.Message):
    __slots__ = ("secrets", "next_page_token")
    SECRETS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    secrets: _containers.RepeatedCompositeFieldContainer[SecretMetadata]
    next_page_token: str
    def __init__(self, secrets: _Optional[_Iterable[_Union[SecretMetadata, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class DeleteSecretRequest(_message.Message):
    __slots__ = ("path",)
    PATH_FIELD_NUMBER: _ClassVar[int]
    path: str
    def __init__(self, path: _Optional[str] = ...) -> None: ...

class DeleteSecretResponse(_message.Message):
    __slots__ = ("revision",)
    REVISION_FIELD_NUMBER: _ClassVar[int]
    revision: int
    def __init__(self, revision: _Optional[int] = ...) -> None: ...

class DisableSecretRequest(_message.Message):
    __slots__ = ("path", "version", "enable")
    PATH_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    ENABLE_FIELD_NUMBER: _ClassVar[int]
    path: str
    version: int
    enable: bool
    def __init__(self, path: _Optional[str] = ..., version: _Optional[int] = ..., enable: _Optional[bool] = ...) -> None: ...

class DisableSecretResponse(_message.Message):
    __slots__ = ("revision",)
    REVISION_FIELD_NUMBER: _ClassVar[int]
    revision: int
    def __init__(self, revision: _Optional[int] = ...) -> None: ...

class DestroySecretVersionRequest(_message.Message):
    __slots__ = ("path", "version")
    PATH_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    path: str
    version: int
    def __init__(self, path: _Optional[str] = ..., version: _Optional[int] = ...) -> None: ...

class DestroySecretVersionResponse(_message.Message):
    __slots__ = ("revision",)
    REVISION_FIELD_NUMBER: _ClassVar[int]
    revision: int
    def __init__(self, revision: _Optional[int] = ...) -> None: ...

class GetSecretMetadataRequest(_message.Message):
    __slots__ = ("path",)
    PATH_FIELD_NUMBER: _ClassVar[int]
    path: str
    def __init__(self, path: _Optional[str] = ...) -> None: ...

class GetSecretMetadataResponse(_message.Message):
    __slots__ = ("secret",)
    SECRET_FIELD_NUMBER: _ClassVar[int]
    secret: SecretMetadata
    def __init__(self, secret: _Optional[_Union[SecretMetadata, _Mapping]] = ...) -> None: ...

class PromoteSecretVersionRequest(_message.Message):
    __slots__ = ("path", "version")
    PATH_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    path: str
    version: int
    def __init__(self, path: _Optional[str] = ..., version: _Optional[int] = ...) -> None: ...

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
    __slots__ = ("client_name", "paths", "last_seen_revision", "acked_revision")
    CLIENT_NAME_FIELD_NUMBER: _ClassVar[int]
    PATHS_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_REVISION_FIELD_NUMBER: _ClassVar[int]
    ACKED_REVISION_FIELD_NUMBER: _ClassVar[int]
    client_name: str
    paths: _containers.RepeatedScalarFieldContainer[str]
    last_seen_revision: int
    acked_revision: int
    def __init__(self, client_name: _Optional[str] = ..., paths: _Optional[_Iterable[str]] = ..., last_seen_revision: _Optional[int] = ..., acked_revision: _Optional[int] = ...) -> None: ...

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
    __slots__ = ("path", "change_type", "value", "content_type", "version", "label")
    PATH_FIELD_NUMBER: _ClassVar[int]
    CHANGE_TYPE_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    path: str
    change_type: str
    value: str
    content_type: str
    version: int
    label: str
    def __init__(self, path: _Optional[str] = ..., change_type: _Optional[str] = ..., value: _Optional[str] = ..., content_type: _Optional[str] = ..., version: _Optional[int] = ..., label: _Optional[str] = ...) -> None: ...

class SecretMetadataChange(_message.Message):
    __slots__ = ("path", "change_type", "version", "label")
    PATH_FIELD_NUMBER: _ClassVar[int]
    CHANGE_TYPE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    path: str
    change_type: str
    version: int
    label: str
    def __init__(self, path: _Optional[str] = ..., change_type: _Optional[str] = ..., version: _Optional[int] = ..., label: _Optional[str] = ...) -> None: ...

class Heartbeat(_message.Message):
    __slots__ = ("server_time_unix_ms",)
    SERVER_TIME_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    server_time_unix_ms: int
    def __init__(self, server_time_unix_ms: _Optional[int] = ...) -> None: ...

class WatchParameterRequest(_message.Message):
    __slots__ = ("path", "last_seen_revision")
    PATH_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_REVISION_FIELD_NUMBER: _ClassVar[int]
    path: str
    last_seen_revision: int
    def __init__(self, path: _Optional[str] = ..., last_seen_revision: _Optional[int] = ...) -> None: ...

class WatchNamespaceRequest(_message.Message):
    __slots__ = ("path_prefix", "last_seen_revision")
    PATH_PREFIX_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_REVISION_FIELD_NUMBER: _ClassVar[int]
    path_prefix: str
    last_seen_revision: int
    def __init__(self, path_prefix: _Optional[str] = ..., last_seen_revision: _Optional[int] = ...) -> None: ...

class Namespace(_message.Message):
    __slots__ = ("path", "description", "created_by", "created_at_unix_ms")
    PATH_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    path: str
    description: str
    created_by: str
    created_at_unix_ms: int
    def __init__(self, path: _Optional[str] = ..., description: _Optional[str] = ..., created_by: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ...) -> None: ...

class CreateNamespaceRequest(_message.Message):
    __slots__ = ("path", "description")
    PATH_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    path: str
    description: str
    def __init__(self, path: _Optional[str] = ..., description: _Optional[str] = ...) -> None: ...

class CreateNamespaceResponse(_message.Message):
    __slots__ = ("namespace",)
    NAMESPACE_FIELD_NUMBER: _ClassVar[int]
    namespace: Namespace
    def __init__(self, namespace: _Optional[_Union[Namespace, _Mapping]] = ...) -> None: ...

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
    __slots__ = ("operation", "path")
    OPERATION_FIELD_NUMBER: _ClassVar[int]
    PATH_FIELD_NUMBER: _ClassVar[int]
    operation: str
    path: str
    def __init__(self, operation: _Optional[str] = ..., path: _Optional[str] = ...) -> None: ...

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

class Identity(_message.Message):
    __slots__ = ("name", "kind", "disabled", "created_at_unix_ms")
    NAME_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    DISABLED_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    name: str
    kind: str
    disabled: bool
    created_at_unix_ms: int
    def __init__(self, name: _Optional[str] = ..., kind: _Optional[str] = ..., disabled: _Optional[bool] = ..., created_at_unix_ms: _Optional[int] = ...) -> None: ...

class CreateIdentityRequest(_message.Message):
    __slots__ = ("name", "kind")
    NAME_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    name: str
    kind: str
    def __init__(self, name: _Optional[str] = ..., kind: _Optional[str] = ...) -> None: ...

class CreateIdentityResponse(_message.Message):
    __slots__ = ("identity", "token")
    IDENTITY_FIELD_NUMBER: _ClassVar[int]
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    identity: Identity
    token: str
    def __init__(self, identity: _Optional[_Union[Identity, _Mapping]] = ..., token: _Optional[str] = ...) -> None: ...

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

class AuditEvent(_message.Message):
    __slots__ = ("id", "event_type", "actor_identity", "actor_type", "resource_type", "resource_path", "resource_version", "decision", "source_ip", "user_agent", "request_id", "created_at_unix_ms", "metadata_json")
    ID_FIELD_NUMBER: _ClassVar[int]
    EVENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    ACTOR_IDENTITY_FIELD_NUMBER: _ClassVar[int]
    ACTOR_TYPE_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_TYPE_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_PATH_FIELD_NUMBER: _ClassVar[int]
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
    resource_path: str
    resource_version: int
    decision: str
    source_ip: str
    user_agent: str
    request_id: str
    created_at_unix_ms: int
    metadata_json: str
    def __init__(self, id: _Optional[int] = ..., event_type: _Optional[str] = ..., actor_identity: _Optional[str] = ..., actor_type: _Optional[str] = ..., resource_type: _Optional[str] = ..., resource_path: _Optional[str] = ..., resource_version: _Optional[int] = ..., decision: _Optional[str] = ..., source_ip: _Optional[str] = ..., user_agent: _Optional[str] = ..., request_id: _Optional[str] = ..., created_at_unix_ms: _Optional[int] = ..., metadata_json: _Optional[str] = ...) -> None: ...

class ListAuditEventsRequest(_message.Message):
    __slots__ = ("path_prefix", "actor_identity", "event_type", "from_unix_ms", "to_unix_ms", "page_size", "page_token")
    PATH_PREFIX_FIELD_NUMBER: _ClassVar[int]
    ACTOR_IDENTITY_FIELD_NUMBER: _ClassVar[int]
    EVENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    FROM_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    TO_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    path_prefix: str
    actor_identity: str
    event_type: str
    from_unix_ms: int
    to_unix_ms: int
    page_size: int
    page_token: str
    def __init__(self, path_prefix: _Optional[str] = ..., actor_identity: _Optional[str] = ..., event_type: _Optional[str] = ..., from_unix_ms: _Optional[int] = ..., to_unix_ms: _Optional[int] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListAuditEventsResponse(_message.Message):
    __slots__ = ("events", "next_page_token")
    EVENTS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    events: _containers.RepeatedCompositeFieldContainer[AuditEvent]
    next_page_token: str
    def __init__(self, events: _Optional[_Iterable[_Union[AuditEvent, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class Subscriber(_message.Message):
    __slots__ = ("client_name", "instance_id", "identity", "paths", "remote_addr", "connected_at_unix_ms", "last_heartbeat_unix_ms", "last_acked_revision")
    CLIENT_NAME_FIELD_NUMBER: _ClassVar[int]
    INSTANCE_ID_FIELD_NUMBER: _ClassVar[int]
    IDENTITY_FIELD_NUMBER: _ClassVar[int]
    PATHS_FIELD_NUMBER: _ClassVar[int]
    REMOTE_ADDR_FIELD_NUMBER: _ClassVar[int]
    CONNECTED_AT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LAST_HEARTBEAT_UNIX_MS_FIELD_NUMBER: _ClassVar[int]
    LAST_ACKED_REVISION_FIELD_NUMBER: _ClassVar[int]
    client_name: str
    instance_id: str
    identity: str
    paths: _containers.RepeatedScalarFieldContainer[str]
    remote_addr: str
    connected_at_unix_ms: int
    last_heartbeat_unix_ms: int
    last_acked_revision: int
    def __init__(self, client_name: _Optional[str] = ..., instance_id: _Optional[str] = ..., identity: _Optional[str] = ..., paths: _Optional[_Iterable[str]] = ..., remote_addr: _Optional[str] = ..., connected_at_unix_ms: _Optional[int] = ..., last_heartbeat_unix_ms: _Optional[int] = ..., last_acked_revision: _Optional[int] = ...) -> None: ...

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
