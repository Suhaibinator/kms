"""An in-process fake KMS gRPC server for exercising the SDK end to end.

It implements enough of ParameterService, SecretService, WatchService, and the
AdminService WhoAmI RPC to test namespace-native reads, writes, auth-metadata
propagation, error mapping, pagination, caching, hot reload, namespace
subscription, and WhoAmI-based namespace discovery. It is intentionally simple,
not a faithful store.

Resources are keyed by an explicit ``(env, app, key)`` tuple. Test helpers accept
either explicit ``env, app, key`` or an absolute ``/env/app/key`` display path.
"""

from __future__ import annotations

import threading
import time
from concurrent import futures
from dataclasses import dataclass
from typing import Dict, List, Optional, Tuple

import grpc

from kms_paramstore._gen import kms_pb2, kms_pb2_grpc

_RefKey = Tuple[str, str, str]  # (env, app, key)
_NSKey = Tuple[str, str]  # (env, app)


def _split_path(path: str) -> _RefKey:
    """Split an absolute ``/env/app/key`` display path into ``(env, app, key)``."""
    assert path.startswith("/"), path
    env, app, key = path[1:].split("/", 2)
    return env, app, key


def _rk_from_ref(ref) -> _RefKey:
    return (ref.namespace.env, ref.namespace.app, ref.key)


def _proto_ref(rk: _RefKey) -> kms_pb2.ResourceRef:
    env, app, key = rk
    return kms_pb2.ResourceRef(namespace=kms_pb2.NamespaceRef(env=env, app=app), key=key)


@dataclass
class _Subscription:
    namespaces: List[_NSKey]
    queue: "list"
    cond: threading.Condition
    acked: int = 0
    closed: bool = False


class FakeStore:
    def __init__(
        self,
        *,
        require_bearer: Optional[str] = None,
        whoami_name: str = "test-client",
        whoami_kind: str = "client",
        whoami_namespace: Optional[str] = None,  # "env/app" or None (unbound)
        whoami_auth_method: str = "token",
    ) -> None:
        self.lock = threading.Lock()
        self.revision = 0
        # (env, app, key) -> list of (value, content_type); index+1 is the version
        self.params: Dict[_RefKey, List[tuple]] = {}
        # Test-only secret state. Binding credentials are retained solely so
        # this in-process fake can exercise credential and cohort behavior.
        self.secrets: Dict[_RefKey, dict] = {}
        self.subs: List[_Subscription] = []
        self.require_bearer = require_bearer
        self.whoami_name = whoami_name
        self.whoami_kind = whoami_kind
        self.whoami_namespace = whoami_namespace
        self.whoami_auth_method = whoami_auth_method

    # --- helpers -----------------------------------------------------------

    def _next_rev(self) -> int:
        self.revision += 1
        return self.revision

    def _rk(self, env, app=None, key=None) -> _RefKey:
        # Accept either (path) or (env, app, key).
        if app is None and key is None:
            return _split_path(env)
        return (env, app, key)

    def put_param(self, env, app=None, key=None, value="", content_type: str = "string") -> tuple:
        rk = self._rk(env, app, key)
        with self.lock:
            versions = self.params.setdefault(rk, [])
            versions.append((value, content_type or "string"))
            version = len(versions)
            rev = self._next_rev()
        self._broadcast(kms_pb2.SubscribeEvent(
            change=kms_pb2.ParameterChange(
                ref=_proto_ref(rk), change_type="put", value=value,
                content_type=content_type or "string", version=version,
            ),
            revision=rev,
        ), rk)
        return version, rev

    def put_param_path(self, path: str, value: str, content_type: str = "string") -> tuple:
        return self.put_param(path, value=value, content_type=content_type)

    def current_param(self, rk: _RefKey):
        versions = self.params.get(rk)
        if not versions:
            return None
        value, ct = versions[-1]
        return kms_pb2.Parameter(
            ref=_proto_ref(rk), value=value, content_type=ct, version=len(versions),
            labels={"current": len(versions)},
        )

    def _broadcast(self, ev, rk: Optional[_RefKey]) -> None:
        with self.lock:
            subs = list(self.subs)
        for sub in subs:
            # A subscriber to a namespace receives every change in it.
            deliver = rk is None or (rk[0], rk[1]) in sub.namespaces
            if deliver:
                with sub.cond:
                    sub.queue.append(ev)
                    sub.cond.notify()

    def heartbeat(self) -> int:
        with self.lock:
            rev = self.revision
        self._broadcast(kms_pb2.SubscribeEvent(
            heartbeat=kms_pb2.Heartbeat(server_time_unix_ms=int(time.time() * 1000)),
            revision=rev,
        ), None)  # heartbeats go to all subscriptions
        return rev


def _check_bearer(store: FakeStore, context) -> bool:
    if not store.require_bearer:
        return True
    md = dict(context.invocation_metadata())
    return md.get("authorization") == "Bearer " + store.require_bearer


class ParameterServicer(kms_pb2_grpc.ParameterServiceServicer):
    def __init__(self, store: FakeStore) -> None:
        self.store = store

    def GetParameter(self, request, context):
        if not _check_bearer(self.store, context):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "missing or invalid token")
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            versions = self.store.params.get(rk)
            if not versions:
                context.abort(grpc.StatusCode.NOT_FOUND, f"parameter {rk} not found")
            if request.version:
                if request.version > len(versions):
                    context.abort(grpc.StatusCode.NOT_FOUND, "version not found")
                value, ct = versions[request.version - 1]
                ver = request.version
            else:
                value, ct = versions[-1]
                ver = len(versions)
        return kms_pb2.GetParameterResponse(
            parameter=kms_pb2.Parameter(
                ref=_proto_ref(rk), value=value, content_type=ct, version=ver,
                labels={"current": len(versions)},
            )
        )

    def PutParameter(self, request, context):
        rk = _rk_from_ref(request.ref)
        version, rev = self.store.put_param(rk[0], rk[1], rk[2], value=request.value, content_type=request.content_type)
        return kms_pb2.PutParameterResponse(version=version, revision=rev)

    def ListParameters(self, request, context):
        ns = (request.namespace.env, request.namespace.app)
        prefix = request.key_prefix
        with self.store.lock:
            keys = sorted(
                rk for rk in self.store.params
                if rk[0] == ns[0] and rk[1] == ns[1] and (not prefix or rk[2].startswith(prefix))
            )
        page_size = request.page_size or 100
        start = int(request.page_token) if request.page_token else 0
        window = keys[start:start + page_size]
        next_token = str(start + page_size) if start + page_size < len(keys) else ""
        params = [self.store.current_param(rk) for rk in window]
        return kms_pb2.ListParametersResponse(parameters=params, next_page_token=next_token)

    def DeleteParameter(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            if rk not in self.store.params:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            del self.store.params[rk]
            rev = self.store._next_rev()
        return kms_pb2.DeleteParameterResponse(revision=rev)

    def GetParameterMetadata(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            versions = self.store.params.get(rk)
            if not versions:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            infos = [
                kms_pb2.ParameterVersionInfo(
                    version=index + 1, content_type=content_type,
                    state="enabled", created_by="fake",
                )
                for index, (_value, content_type) in enumerate(versions)
            ]
        return kms_pb2.GetParameterMetadataResponse(
            ref=_proto_ref(rk), content_type=versions[-1][1],
            labels={"current": len(versions)}, versions=infos,
        )


class SecretServicer(kms_pb2_grpc.SecretServiceServicer):
    def __init__(self, store: FakeStore) -> None:
        self.store = store

    def GetSecret(self, request, context):
        if not _check_bearer(self.store, context):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "missing or invalid token")
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, f"secret {rk} not found")
            self._extend_version_state(sec)
            if not sec.get("promoted", False) and sec["current_version"] < len(sec["versions"]):
                sec["current_version"] = len(sec["versions"])
            version = request.version or sec["current_version"]
            if version < 1 or version > len(sec["versions"]):
                context.abort(grpc.StatusCode.NOT_FOUND, "version not found")
            if sec["states"][version - 1] != "enabled":
                context.abort(grpc.StatusCode.FAILED_PRECONDITION, "version is not enabled")
            if sec["has_tokens"][version - 1] and request.secret_token != sec["token"]:
                context.abort(grpc.StatusCode.PERMISSION_DENIED, "secret credential unavailable")
            binding_key = sec["binding_keys"][version - 1]
            if binding_key and request.binding_key != binding_key:
                context.abort(grpc.StatusCode.PERMISSION_DENIED, "secret credential unavailable")
            value, ct = sec["versions"][version - 1]
        return kms_pb2.GetSecretResponse(
            ref=_proto_ref(rk), version=version, value=value, content_type=ct,
        )

    def PutSecret(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            token = ""
            if sec is None:
                token = "tok-" + "_".join(rk) if request.generate_access_token else ""
                sec = {
                    "value": request.value, "content_type": request.content_type or "application/octet-stream",
                    "token": token, "versions": [], "states": [],
                    "binding_keys": [], "has_tokens": [], "expires": [],
                    "metadata": [], "current_version": 0, "previous_version": 0,
                    "promoted": False,
                }
                self.store.secrets[rk] = sec
            else:
                if request.generate_access_token:
                    token = "tok2-" + "_".join(rk)
                    sec["token"] = token
            sec["versions"].append((request.value, request.content_type or sec["content_type"]))
            sec["states"].append("enabled")
            sec["binding_keys"].append(request.binding_key)
            sec["has_tokens"].append(bool(sec["token"]))
            sec["expires"].append(request.expires_at_unix_ms)
            sec["metadata"].append(request.metadata_json or "{}")
            version = len(sec["versions"])
            sec["value"] = request.value
            sec["content_type"] = sec["versions"][-1][1]
            sec["current_version"] = version
            sec["promoted"] = False
            rev = self.store._next_rev()
        self.store._broadcast(kms_pb2.SubscribeEvent(
            secret_change=kms_pb2.SecretMetadataChange(ref=_proto_ref(rk), change_type="put", version=version),
            revision=rev,
        ), rk)
        return kms_pb2.PutSecretResponse(version=version, revision=rev, access_token=token)

    def GetSecretMetadata(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            self._extend_version_state(sec)
            versions = [kms_pb2.SecretVersionInfo(
                version=i + 1, state=sec["states"][i],
                expires_at_unix_ms=sec["expires"][i],
                metadata_json=sec["metadata"][i],
                bound=bool(sec["binding_keys"][i]),
                has_access_token=sec["has_tokens"][i],
            ) for i in range(len(sec["versions"]))]
            current = sec["current_version"] - 1
            meta = kms_pb2.SecretMetadata(
                ref=_proto_ref(rk), content_type=sec["content_type"],
                bound=bool(sec["binding_keys"][current]),
                has_access_token=bool(sec["token"]),
                metadata_json=sec["metadata"][current],
                labels={
                    "current": sec["current_version"],
                    **({"previous": sec.get("previous_version", 0)} if sec.get("previous_version") else {}),
                }, versions=versions,
            )
        return kms_pb2.GetSecretMetadataResponse(secret=meta)

    def ListSecrets(self, request, context):
        ns = (request.namespace.env, request.namespace.app)
        with self.store.lock:
            keys = sorted(
                rk for rk in self.store.secrets
                if rk[:2] == ns and (not request.key_prefix or rk[2].startswith(request.key_prefix))
            )
            page_size = request.page_size or 100
            start = int(request.page_token) if request.page_token else 0
            window = keys[start:start + page_size]
            next_token = str(start + page_size) if start + page_size < len(keys) else ""
            items = []
            for rk in window:
                sec = self.store.secrets[rk]
                self._extend_version_state(sec)
                current = sec["current_version"] - 1
                items.append(kms_pb2.SecretMetadata(
                    ref=_proto_ref(rk), content_type=sec["content_type"],
                    bound=bool(sec["binding_keys"][current]),
                    has_access_token=bool(sec["token"]),
                    metadata_json=sec["metadata"][current],
                    labels={"current": sec["current_version"]},
                    versions=[
                        kms_pb2.SecretVersionInfo(
                            version=i + 1, state=state,
                            expires_at_unix_ms=sec["expires"][i],
                            metadata_json=sec["metadata"][i],
                            bound=bool(sec["binding_keys"][i]),
                            has_access_token=sec["has_tokens"][i],
                        )
                        for i, state in enumerate(sec["states"])
                    ],
                ))
        return kms_pb2.ListSecretsResponse(secrets=items, next_page_token=next_token)

    def DeleteSecret(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            if rk not in self.store.secrets:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            del self.store.secrets[rk]
            rev = self.store._next_rev()
        return kms_pb2.DeleteSecretResponse(revision=rev)

    def DisableSecret(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            indexes = range(len(sec["states"])) if request.version == 0 else (request.version - 1,)
            for index in indexes:
                if index < 0 or index >= len(sec["states"]):
                    context.abort(grpc.StatusCode.NOT_FOUND, "version not found")
                sec["states"][index] = "enabled" if request.enable else "disabled"
            rev = self.store._next_rev()
        return kms_pb2.DisableSecretResponse(revision=rev)

    def DestroySecretVersion(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None or request.version < 1 or request.version > len(sec["versions"]):
                context.abort(grpc.StatusCode.NOT_FOUND, "version not found")
            sec["states"][request.version - 1] = "destroyed"
            sec["versions"][request.version - 1] = (b"", sec["versions"][request.version - 1][1])
            rev = self.store._next_rev()
        return kms_pb2.DestroySecretVersionResponse(revision=rev)

    def PromoteSecretVersion(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None or request.version < 1 or request.version > len(sec["versions"]):
                context.abort(grpc.StatusCode.NOT_FOUND, "version not found")
            previous = sec["current_version"]
            sec["current_version"] = request.version
            sec["previous_version"] = previous
            sec["promoted"] = True
            rev = self.store._next_rev()
        return kms_pb2.PromoteSecretVersionResponse(
            current_version=request.version, previous_version=previous, revision=rev
        )

    def BindSecret(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            if request.expected_current_version == 0:
                context.abort(grpc.StatusCode.INVALID_ARGUMENT, "expected current version is required")
            if request.expected_current_version != sec["current_version"]:
                context.abort(grpc.StatusCode.ABORTED, "current version changed")
            sec, index = self._version(
                sec=sec, version=request.expected_current_version, context=context,
            )
            if sec["states"][index] == "destroyed" or sec["binding_keys"][index]:
                context.abort(grpc.StatusCode.FAILED_PRECONDITION, "binding state cannot change")
            current, previous = self._clone_transition(
                sec, index, binding_key=request.binding_key,
            )
            rev = self.store._next_rev()
        return kms_pb2.SecretVersionTransitionResponse(
            current_version=current, previous_version=previous, revision=rev,
        )

    def UnbindSecret(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            if request.expected_current_version == 0:
                context.abort(grpc.StatusCode.INVALID_ARGUMENT, "expected current version is required")
            if request.expected_current_version != sec["current_version"]:
                context.abort(grpc.StatusCode.ABORTED, "current version changed")
            sec, index = self._version(
                sec=sec, version=request.expected_current_version, context=context,
            )
            if sec["states"][index] == "destroyed" or sec["binding_keys"][index] != request.binding_key:
                context.abort(grpc.StatusCode.PERMISSION_DENIED, "secret credential unavailable")
            current, previous = self._clone_transition(sec, index, binding_key="")
            rev = self.store._next_rev()
        return kms_pb2.SecretVersionTransitionResponse(
            current_version=current, previous_version=previous, revision=rev,
        )

    def PreviewSecretBindingCohort(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec, index = self._version(sec=self.store.secrets.get(rk), version=request.anchor_version, context=context)
            versions = self._cohort(sec, index, request.binding_key, context)
            revision = self.store.revision
        return kms_pb2.SecretBindingCohortResponse(
            anchor_version=index + 1, affected_versions=versions, revision=revision,
        )

    def RotateSecretBindingKey(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            if request.expected_current_version == 0:
                context.abort(grpc.StatusCode.INVALID_ARGUMENT, "expected current version is required")
            if request.expected_current_version != sec["current_version"]:
                context.abort(grpc.StatusCode.ABORTED, "current version changed")
            sec, index = self._version(
                sec=sec, version=request.expected_current_version, context=context,
            )
            if not sec["binding_keys"][index] or sec["binding_keys"][index] != request.binding_key:
                context.abort(grpc.StatusCode.PERMISSION_DENIED, "secret credential unavailable")
            if request.binding_key == request.new_binding_key:
                context.abort(grpc.StatusCode.INVALID_ARGUMENT, "new binding key must differ")
            current, previous = self._clone_transition(
                sec, index, binding_key=request.new_binding_key,
            )
            revision = self.store._next_rev()
        return kms_pb2.SecretVersionTransitionResponse(
            current_version=current, previous_version=previous, revision=revision,
        )

    def PurgeSecretBindingCohort(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec, index = self._version(sec=self.store.secrets.get(rk), version=request.anchor_version, context=context)
            versions = self._cohort(sec, index, request.binding_key, context)
            self._check_guard(request, versions, context)
            for version in versions:
                item = version - 1
                sec["versions"][item] = (b"", "")
                sec["states"][item] = "destroyed"
                sec["binding_keys"][item] = ""
                sec["has_tokens"][item] = False
                sec["expires"][item] = 0
                sec["metadata"][item] = ""
            if sec["current_version"] in versions:
                sec["value"] = b""
                sec["content_type"] = ""
            revision = self.store._next_rev()
        return kms_pb2.SecretBindingCohortResponse(
            anchor_version=index + 1, affected_versions=versions, revision=revision,
        )

    def PreviewSecretUnboundVersions(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            self._extend_version_state(sec)
            versions = [
                index + 1 for index, state in enumerate(sec["states"])
                if state != "destroyed" and not sec["binding_keys"][index]
            ]
            if not versions:
                context.abort(grpc.StatusCode.FAILED_PRECONDITION, "no unbound versions")
            revision = self.store.revision
        return kms_pb2.SecretVersionSetResponse(
            affected_versions=versions, revision=revision,
        )

    def PurgeSecretUnboundVersions(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            sec = self.store.secrets.get(rk)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            self._extend_version_state(sec)
            versions = [
                index + 1 for index, state in enumerate(sec["states"])
                if state != "destroyed" and not sec["binding_keys"][index]
            ]
            if not versions:
                context.abort(grpc.StatusCode.FAILED_PRECONDITION, "no unbound versions")
            expected = list(request.expected_affected_versions)
            if request.expected_revision == 0 or not expected:
                context.abort(grpc.StatusCode.INVALID_ARGUMENT, "preview guard required")
            if request.expected_revision != self.store.revision or expected != versions:
                context.abort(grpc.StatusCode.ABORTED, "secret version set changed")
            for version in versions:
                item = version - 1
                sec["versions"][item] = (b"", "")
                sec["states"][item] = "destroyed"
                sec["binding_keys"][item] = ""
                sec["has_tokens"][item] = False
                sec["expires"][item] = 0
                sec["metadata"][item] = ""
            if sec["current_version"] in versions:
                sec["value"] = b""
                sec["content_type"] = ""
            revision = self.store._next_rev()
        return kms_pb2.SecretVersionSetResponse(
            affected_versions=versions, revision=revision,
        )

    @staticmethod
    def _clone_transition(sec: dict, index: int, *, binding_key: str) -> tuple[int, int]:
        previous = index + 1
        sec["versions"].append(sec["versions"][index])
        sec["states"].append(sec["states"][index])
        sec["binding_keys"].append(binding_key)
        sec["has_tokens"].append(sec["has_tokens"][index])
        sec["expires"].append(sec["expires"][index])
        sec["metadata"].append(sec["metadata"][index])
        current = len(sec["versions"])
        sec["previous_version"] = previous
        sec["current_version"] = current
        sec["value"], sec["content_type"] = sec["versions"][current - 1]
        sec["promoted"] = True
        return current, previous

    @staticmethod
    def _extend_version_state(sec: dict) -> None:
        count = len(sec["versions"])
        sec.setdefault("states", [])
        sec.setdefault("binding_keys", [])
        sec.setdefault("has_tokens", [])
        sec.setdefault("expires", [])
        sec.setdefault("metadata", [])
        while len(sec["states"]) < count:
            sec["states"].append("enabled")
        while len(sec["binding_keys"]) < count:
            sec["binding_keys"].append("")
        while len(sec["has_tokens"]) < count:
            sec["has_tokens"].append(bool(sec.get("token")))
        while len(sec["expires"]) < count:
            sec["expires"].append(0)
        while len(sec["metadata"]) < count:
            sec["metadata"].append("{}")

    def _version(self, *, sec, version: int, context):
        if sec is None:
            context.abort(grpc.StatusCode.NOT_FOUND, "not found")
        self._extend_version_state(sec)
        selected = version or sec["current_version"]
        if selected < 1 or selected > len(sec["versions"]):
            context.abort(grpc.StatusCode.NOT_FOUND, "version not found")
        return sec, selected - 1

    @staticmethod
    def _cohort(sec: dict, index: int, binding_key: str, context) -> list[int]:
        if sec["states"][index] == "destroyed" or sec["binding_keys"][index] != binding_key:
            context.abort(grpc.StatusCode.PERMISSION_DENIED, "secret credential unavailable")
        low = index
        while low > 0 and sec["states"][low - 1] != "destroyed" and sec["binding_keys"][low - 1] == binding_key:
            low -= 1
        high = index
        while high + 1 < len(sec["versions"]) and sec["states"][high + 1] != "destroyed" and sec["binding_keys"][high + 1] == binding_key:
            high += 1
        return list(range(low + 1, high + 2))

    def _check_guard(self, request, versions: list[int], context) -> None:
        expected = list(request.expected_affected_versions)
        if request.expected_revision == 0 or not expected:
            context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                "expected revision and affected versions must be supplied together",
            )
        if any(version <= 0 for version in expected) or expected != sorted(set(expected)):
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "invalid preview guard")
        if (
            request.expected_revision != self.store.revision
            or expected != versions
        ):
            context.abort(grpc.StatusCode.ABORTED, "secret version set changed")


class WatchServicer(kms_pb2_grpc.WatchServiceServicer):
    def __init__(self, store: FakeStore) -> None:
        self.store = store

    def Subscribe(self, request_iterator, context):
        it = iter(request_iterator)
        reg = next(it)
        namespaces = [(n.env, n.app) for n in reg.namespaces]
        if not namespaces:
            # Parity with the real server (grpcserver.normalizeNamespaces): an empty
            # subscription is rejected. Keeps the fake honest so a client that sends
            # namespaces=[] fails loudly here instead of "succeeding" against a
            # lenient fake.
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "at least one namespace is required")
        sub = _Subscription(namespaces=namespaces, queue=[], cond=threading.Condition())

        with self.store.lock:
            self.store.subs.append(sub)
            rev = self.store.revision
            snap_params = []
            for rk, versions in self.store.params.items():
                if (rk[0], rk[1]) in namespaces:
                    value, ct = versions[-1]
                    snap_params.append(kms_pb2.Parameter(
                        ref=_proto_ref(rk), value=value, content_type=ct, version=len(versions),
                        labels={"current": len(versions)},
                    ))

        def close():
            with sub.cond:
                sub.closed = True
                sub.cond.notify()
            with self.store.lock:
                if sub in self.store.subs:
                    self.store.subs.remove(sub)

        context.add_callback(close)

        def read_acks():
            try:
                for msg in it:
                    sub.acked = msg.acked_revision
            except Exception:
                pass
            close()

        threading.Thread(target=read_acks, daemon=True).start()

        # Initial snapshot.
        yield kms_pb2.SubscribeEvent(snapshot=kms_pb2.Snapshot(parameters=snap_params), revision=rev)

        while True:
            with sub.cond:
                while not sub.queue and not sub.closed:
                    sub.cond.wait(timeout=1.0)
                    if not context.is_active():
                        return
                if sub.closed and not sub.queue:
                    return
                ev = sub.queue.pop(0)
            yield ev


class AdminServicer(kms_pb2_grpc.AdminServiceServicer):
    def __init__(self, store: FakeStore) -> None:
        self.store = store

    def WhoAmI(self, request, context):
        if not _check_bearer(self.store, context):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "missing or invalid token")
        ns = kms_pb2.NamespaceRef()
        if self.store.whoami_namespace:
            env, _, app = self.store.whoami_namespace.partition("/")
            ns = kms_pb2.NamespaceRef(env=env, app=app)
        return kms_pb2.WhoAmIResponse(
            name=self.store.whoami_name,
            kind=self.store.whoami_kind,
            namespace=ns,
            auth_method=self.store.whoami_auth_method,
        )


def start_server(
    *,
    require_bearer: Optional[str] = None,
    whoami_namespace: Optional[str] = None,
    whoami_kind: str = "client",
    server_credentials: Optional[grpc.ServerCredentials] = None,
) -> tuple:
    """Start the fake server on a random localhost port.

    Returns ``(server, address, store)``.
    """
    store = FakeStore(
        require_bearer=require_bearer,
        whoami_namespace=whoami_namespace,
        whoami_kind=whoami_kind,
    )
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=16))
    kms_pb2_grpc.add_ParameterServiceServicer_to_server(ParameterServicer(store), server)
    kms_pb2_grpc.add_SecretServiceServicer_to_server(SecretServicer(store), server)
    kms_pb2_grpc.add_WatchServiceServicer_to_server(WatchServicer(store), server)
    kms_pb2_grpc.add_AdminServiceServicer_to_server(AdminServicer(store), server)
    if server_credentials is None:
        port = server.add_insecure_port("localhost:0")
    else:
        port = server.add_secure_port("localhost:0", server_credentials)
    if port == 0:
        raise RuntimeError("failed to bind the fake gRPC server")
    server.start()
    return server, f"localhost:{port}", store
