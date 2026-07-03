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
        # (env, app, key) -> dict(value, content_type, token, client_bound, versions)
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
            md = dict(context.invocation_metadata())
            if sec["token"]:
                if md.get("x-kms-secret-token") != sec["token"]:
                    context.abort(grpc.StatusCode.PERMISSION_DENIED, "secret token required")
            version = request.version or len(sec["versions"])
            if version < 1 or version > len(sec["versions"]):
                context.abort(grpc.StatusCode.NOT_FOUND, "version not found")
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
                    "token": token, "client_bound": request.client_bound, "versions": [],
                }
                self.store.secrets[rk] = sec
            else:
                if request.generate_access_token:
                    token = "tok2-" + "_".join(rk)
                    sec["token"] = token
            sec["versions"].append((request.value, request.content_type or sec["content_type"]))
            version = len(sec["versions"])
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
            versions = [
                kms_pb2.SecretVersionInfo(version=i + 1, state="enabled")
                for i in range(len(sec["versions"]))
            ]
            meta = kms_pb2.SecretMetadata(
                ref=_proto_ref(rk), content_type=sec["content_type"],
                client_bound=sec["client_bound"], has_access_token=bool(sec["token"]),
                labels={"current": len(sec["versions"])}, versions=versions,
            )
        return kms_pb2.GetSecretMetadataResponse(secret=meta)

    def DeleteSecret(self, request, context):
        rk = _rk_from_ref(request.ref)
        with self.store.lock:
            if rk not in self.store.secrets:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            del self.store.secrets[rk]
            rev = self.store._next_rev()
        return kms_pb2.DeleteSecretResponse(revision=rev)


class WatchServicer(kms_pb2_grpc.WatchServiceServicer):
    def __init__(self, store: FakeStore) -> None:
        self.store = store

    def Subscribe(self, request_iterator, context):
        it = iter(request_iterator)
        reg = next(it)
        namespaces = [(n.env, n.app) for n in reg.namespaces]
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
    port = server.add_insecure_port("localhost:0")
    server.start()
    return server, f"localhost:{port}", store
