from __future__ import annotations

from dataclasses import FrozenInstanceError
from types import SimpleNamespace
from unittest import mock

import grpc
import pytest

from kms_paramstore import ConfigError, Page, ParamStoreError, RateLimitedError
from kms_paramstore.cache import Cache
from kms_paramstore.errors import map_grpc_error
from kms_paramstore._gen import kms_pb2
from tests.conftest import NS


def test_parameter_info_metadata_and_immutable_page(client):
    client.put_parameter("settings/rate", "10", content_type="integer")
    client.put_parameter("settings/rate", "20", content_type="integer")

    info = client.get_parameter_info("settings/rate", version=1)
    assert (info.value, info.version, info.path) == ("10", 1, "/prod/app/settings/rate")
    with pytest.raises(FrozenInstanceError):
        info.value = "mutated"
    with pytest.raises(TypeError):
        info.labels["current"] = 99

    metadata = client.get_parameter_metadata("settings/rate")
    assert metadata.path == "/prod/app/settings/rate"
    assert [version.version for version in metadata.versions] == [1, 2]

    page = client.list_parameters(NS, key_prefix="settings/")
    assert isinstance(page, Page)
    assert tuple(parameter.key for parameter in page.items) == ("settings/rate",)


def test_secret_inventory_and_version_lifecycle(client):
    client.put_secret("inventory/key", b"one", content_type="text/plain")
    client.put_secret("inventory/key", b"two", content_type="text/plain")

    page = client.list_secrets(NS, key_prefix="inventory/", page_size=1)
    assert [item.key for item in page.items] == ["inventory/key"]
    assert page.items[0].versions[0].state == "enabled"

    client.set_secret_enabled("inventory/key", False, version=2)
    with pytest.raises(Exception) as disabled:
        client.get_secret("inventory/key", version=2)
    assert getattr(disabled.value, "code", None) == "failed_precondition"

    client.set_secret_enabled("inventory/key", True, version=2)
    promoted = client.promote_secret_version("inventory/key", 1)
    assert promoted.current_version == 1
    assert promoted.previous_version == 2
    assert client.get_secret("inventory/key").value == b"one"

    client.destroy_secret_version("inventory/key", 2)
    assert client.get_secret_metadata("inventory/key").versions[1].state == "destroyed"


@pytest.mark.parametrize("version", [-1, 2**64, True])
def test_uint64_inputs_are_validated_before_rpc(client, version):
    with pytest.raises(ConfigError):
        client.get_parameter("key", version=version)
    with pytest.raises(ConfigError):
        client.destroy_secret_version("key", version)


def test_watch_status_is_value_free_and_immutable(client):
    status = client.watch_status
    assert status.state == "idle"
    assert status.current_revision == 0
    assert not hasattr(status, "value")
    with pytest.raises(FrozenInstanceError):
        status.state = "connected"


class _StatusError(grpc.RpcError, grpc.Call):
    def __init__(self, code, details):
        self._code = code
        self._details = details

    def code(self):
        return self._code

    def details(self):
        return self._details

    def initial_metadata(self):
        return None

    def trailing_metadata(self):
        return None

    def is_active(self):
        return False

    def time_remaining(self):
        return 0

    def cancel(self):
        return False

    def add_callback(self, callback):
        return False

    def cancelled(self):
        return False


def test_errors_expose_bounded_programmatic_codes():
    limited = map_grpc_error(_StatusError(grpc.StatusCode.RESOURCE_EXHAUSTED, "budget spent"))
    assert isinstance(limited, RateLimitedError)
    assert limited.code == "resource_exhausted"
    assert limited.grpc_code is grpc.StatusCode.RESOURCE_EXHAUSTED

    unavailable = map_grpc_error(_StatusError(grpc.StatusCode.UNAVAILABLE, "try later"))
    assert type(unavailable) is ParamStoreError
    assert unavailable.code == "unavailable"
    assert unavailable.grpc_code is grpc.StatusCode.UNAVAILABLE


def test_cache_invalidation_fences_inflight_stale_read():
    cache = Cache(60)
    token = cache.begin_parameter_read("/prod/app/key")
    cache.invalidate_param("/prod/app/key")
    cache.put_param_if_unchanged(token, 0, "", "stale")
    assert cache.get_param("/prod/app/key", 0, "") is None


def test_verify_defaults_validates_request_and_hostile_response():
    from kms_paramstore import Client

    client = Client(channel=mock.MagicMock())
    digest = "a" * 64
    valid = kms_pb2.VerifyReleaseDefaultsResponse(
        name="app", version=2, activation_revision=9, schema_matches=True,
        entries=[kms_pb2.VerifyEntryVerdict(alias="settings", verdict="match")],
        match_count=1,
    )
    try:
        client._release_stub = SimpleNamespace(VerifyReleaseDefaults=lambda *args, **kwargs: valid)
        result = client.verify_release_defaults(
            namespace="prod/app", schema_sha256=digest,
            entries=[{"alias": "settings", "content_type": "json", "sha256": digest}],
        )
        assert result.passed
        assert result.entries[0].verdict == "match"

        hostile = kms_pb2.VerifyReleaseDefaultsResponse(
            name="app", entries=[kms_pb2.VerifyEntryVerdict(alias="settings", verdict="match")],
            differs_count=1,
        )
        client._release_stub = SimpleNamespace(VerifyReleaseDefaults=lambda *args, **kwargs: hostile)
        with pytest.raises(ParamStoreError, match="count disagrees") as failure:
            client.verify_release_defaults(
                namespace="prod/app",
                entries=[{"alias": "settings", "content_type": "json", "sha256": digest}],
            )
        assert failure.value.code == "internal"

        client._release_stub = SimpleNamespace(
            VerifyReleaseDefaults=lambda *args, **kwargs: (_ for _ in ()).throw(
                _StatusError(grpc.StatusCode.RESOURCE_EXHAUSTED, "budget spent")
            )
        )
        with pytest.raises(RateLimitedError):
            client.verify_release_defaults(
                namespace="prod/app",
                entries=[{"alias": "settings", "content_type": "json", "sha256": digest}],
            )
    finally:
        client.close()


def test_apply_defaults_rejects_hostile_response():
    from kms_paramstore import Client

    client = Client(channel=mock.MagicMock())
    response = kms_pb2.ApplyApplicationDefaultsResponse(
        plan_digest="plan", executed=False,
        entries=[kms_pb2.DefaultsApplyEntry(alias="settings", key="settings", status="surprise")],
    )
    client._admin_stub = SimpleNamespace(ApplyApplicationDefaults=lambda *args, **kwargs: response)
    try:
        with pytest.raises(ParamStoreError, match="entry 0 is invalid") as failure:
            client.apply_application_defaults(namespace="prod/app", artifact=b"{}")
        assert failure.value.code == "internal"
    finally:
        client.close()
