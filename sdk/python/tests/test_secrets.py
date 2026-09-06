from __future__ import annotations

import pytest

from kms_paramstore import (
    Client,
    ConfigError,
    FailedPreconditionError,
    NotFoundError,
    PermissionDeniedError,
    Secret,
)
from tests.conftest import NS


def test_put_and_get_secret(client):
    res = client.put_secret("key", b"s3cr3t", content_type="text/plain")
    assert res.version == 1
    got = client.get_secret("key")
    assert isinstance(got, Secret)
    assert got.value == b"s3cr3t"
    assert got.string_value == "s3cr3t"
    assert got.namespace == "prod/app"
    assert got.key == "key"
    assert got.path == "/prod/app/key"
    assert got.version == 1
    # The returned object redacts.
    assert str(got) == "[REDACTED]"


def test_put_secret_accepts_str_value(client):
    client.put_secret("str", "as-string")
    assert client.get_secret("str").value == b"as-string"


def test_get_secret_not_found(client):
    with pytest.raises(NotFoundError):
        client.get_secret("nope")



def test_secret_metadata(client, server):
    client.put_secret("meta", b"v")
    info = client.get_secret_metadata("meta")
    assert info.key == "meta"
    assert info.path == "/prod/app/meta"
    assert info.bound is False
    assert info.versions[0].bound is False
    assert len(info.versions) == 1
    exact = client._get_secret_metadata_version("meta", version=1)
    assert [version.version for version in exact.versions] == [1]
    assert server[1].secret_metadata_versions == [0, 1]


def test_binding_key_lifecycle_results_are_immutable(client):
    from dataclasses import FrozenInstanceError

    old_key = "o" * 32
    new_key = "n" * 32
    put = client.put_secret(
        "bound", b"v1", binding_key=old_key,
    )
    with pytest.raises(PermissionDeniedError):
        client.get_secret("bound")
    fetched = client.get_secret(
        "bound", binding_key=old_key,
    )
    assert fetched.value == b"v1"
    assert fetched.bind_key == ""
    info = client.get_secret_metadata("bound")
    assert info.bound and info.versions[0].bound

    rotated = client.rotate_secret_binding_key(
        "bound", expected_current_version=1, binding_key=old_key,
        new_binding_key=new_key,
    )
    assert (rotated.current_version, rotated.previous_version) == (2, 1)
    with pytest.raises(FrozenInstanceError):
        rotated.revision = 0  # type: ignore[misc]
    assert client.get_secret(
        "bound", binding_key=new_key,
    ).value == b"v1"

    unbound = client.unbind_secret(
        "bound", expected_current_version=2, binding_key=new_key,
    )
    assert (unbound.current_version, unbound.previous_version) == (3, 2)
    assert client.get_secret("bound").value == b"v1"
    assert client.get_secret(
        "bound", version=1, binding_key=old_key,
    ).value == b"v1"
    old_preview = client.preview_secret_binding_cohort(
        "bound", anchor_version=1, binding_key=old_key,
    )
    old_purge = client.purge_secret_binding_cohort(
        "bound", anchor_version=1, binding_key=old_key,
        expected_revision=old_preview.revision,
        expected_affected_versions=old_preview.affected_versions,
    )
    assert old_purge.affected_versions == (1,)

    bound_preview = client.preview_secret_binding_cohort(
        "bound", anchor_version=2, binding_key=new_key,
    )
    bound_purge = client.purge_secret_binding_cohort(
        "bound", anchor_version=2, binding_key=new_key,
        expected_revision=bound_preview.revision,
        expected_affected_versions=bound_preview.affected_versions,
    )
    assert bound_purge.affected_versions == (2,)
    with pytest.raises(ConfigError, match="must not be empty"):
        client.purge_secret_binding_cohort(
            "bound", anchor_version=1, binding_key=old_key,
            expected_revision=1, expected_affected_versions=(),
        )

    unbound_preview = client.preview_secret_unbound_versions("bound")
    assert unbound_preview.affected_versions == (3,)
    purged = client.purge_secret_unbound_versions(
        "bound", expected_revision=unbound_preview.revision,
        expected_affected_versions=unbound_preview.affected_versions,
    )
    assert purged.affected_versions == (3,)
    with pytest.raises(FailedPreconditionError):
        client.get_secret("bound", version=1, binding_key=old_key)
    with pytest.raises(FailedPreconditionError):
        client.get_secret(
            "bound", version=1, binding_key=old_key,
        )



def test_delete_secret(client):
    client.put_secret("del", b"v")
    rev = client.delete_secret("del")
    assert rev >= 1
    with pytest.raises(NotFoundError):
        client.get_secret("del")


def test_bearer_token_propagation():
    from tests._fake_server import start_server

    srv, addr, _store = start_server(require_bearer="sekret", whoami_namespace=NS)
    try:
        # Correct token succeeds.
        ok = Client(addr, namespace=NS, token="sekret", insecure=True)
        ok.put_parameter("p", "v")
        assert ok.get_parameter("p") == "v"
        ok.close()

        # Missing token is rejected.
        from kms_paramstore import UnauthenticatedError

        bad = Client(addr, namespace=NS, insecure=True)
        with pytest.raises(UnauthenticatedError):
            bad.get_parameter("p")
        bad.close()
    finally:
        srv.stop(grace=0)
