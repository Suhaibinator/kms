"""An in-process fake KMS gRPC server for exercising the SDK end to end.

It implements enough of ParameterService, SecretService, and WatchService to
test reads, writes, auth-metadata propagation, error mapping, pagination,
caching, and hot reload. It is intentionally simple, not a faithful store.
"""

from __future__ import annotations

import threading
import time
from concurrent import futures
from dataclasses import dataclass, field
from typing import Dict, List, Optional

import grpc

from kms_paramstore._gen import kms_pb2, kms_pb2_grpc


def _match(pattern: str, path: str) -> bool:
    if pattern.endswith("*"):
        return path.startswith(pattern[:-1])
    return pattern == path


@dataclass
class _Subscription:
    paths: List[str]
    queue: "list"
    cond: threading.Condition
    acked: int = 0
    closed: bool = False


class FakeStore:
    def __init__(self, *, require_bearer: Optional[str] = None) -> None:
        self.lock = threading.Lock()
        self.revision = 0
        # path -> list of (value, content_type); index+1 is the version
        self.params: Dict[str, List[tuple]] = {}
        # path -> dict(value: bytes, content_type, token, client_bound, versions)
        self.secrets: Dict[str, dict] = {}
        self.subs: List[_Subscription] = []
        self.require_bearer = require_bearer

    # --- helpers -----------------------------------------------------------

    def _next_rev(self) -> int:
        self.revision += 1
        return self.revision

    def put_param(self, path: str, value: str, content_type: str = "string") -> tuple:
        with self.lock:
            versions = self.params.setdefault(path, [])
            versions.append((value, content_type or "string"))
            version = len(versions)
            rev = self._next_rev()
        self._broadcast(kms_pb2.SubscribeEvent(
            change=kms_pb2.ParameterChange(
                path=path, change_type="put", value=value,
                content_type=content_type or "string", version=version,
            ),
            revision=rev,
        ))
        return version, rev

    def current_param(self, path: str):
        versions = self.params.get(path)
        if not versions:
            return None
        value, ct = versions[-1]
        return kms_pb2.Parameter(
            path=path, value=value, content_type=ct, version=len(versions),
            labels={"current": len(versions)},
        )

    def _broadcast(self, ev) -> None:
        with self.lock:
            subs = list(self.subs)
        for sub in subs:
            # only forward parameter/secret changes matching the subscription
            path = ""
            if ev.WhichOneof("event") == "change":
                path = ev.change.path
            elif ev.WhichOneof("event") == "secret_change":
                path = ev.secret_change.path
            elif ev.WhichOneof("event") == "heartbeat":
                path = None  # deliver to all
            if path is None or any(_match(p, path) for p in sub.paths):
                with sub.cond:
                    sub.queue.append(ev)
                    sub.cond.notify()

    def heartbeat(self) -> int:
        with self.lock:
            rev = self.revision
        self._broadcast(kms_pb2.SubscribeEvent(
            heartbeat=kms_pb2.Heartbeat(server_time_unix_ms=int(time.time() * 1000)),
            revision=rev,
        ))
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
        with self.store.lock:
            versions = self.store.params.get(request.path)
            if not versions:
                context.abort(grpc.StatusCode.NOT_FOUND, f"parameter {request.path} not found")
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
                path=request.path, value=value, content_type=ct, version=ver,
                labels={"current": len(versions)},
            )
        )

    def PutParameter(self, request, context):
        version, rev = self.store.put_param(request.path, request.value, request.content_type)
        return kms_pb2.PutParameterResponse(version=version, revision=rev)

    def ListParameters(self, request, context):
        with self.store.lock:
            paths = sorted(self.store.params.keys())
        prefix = request.path_prefix
        matched = [p for p in paths if not prefix or p == prefix or p.startswith(prefix.rstrip("*"))]
        page_size = request.page_size or 100
        start = int(request.page_token) if request.page_token else 0
        window = matched[start:start + page_size]
        next_token = str(start + page_size) if start + page_size < len(matched) else ""
        params = [self.store.current_param(p) for p in window]
        return kms_pb2.ListParametersResponse(parameters=params, next_page_token=next_token)

    def DeleteParameter(self, request, context):
        with self.store.lock:
            if request.path not in self.store.params:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            del self.store.params[request.path]
            rev = self.store._next_rev()
        return kms_pb2.DeleteParameterResponse(revision=rev)


class SecretServicer(kms_pb2_grpc.SecretServiceServicer):
    def __init__(self, store: FakeStore) -> None:
        self.store = store

    def GetSecret(self, request, context):
        if not _check_bearer(self.store, context):
            context.abort(grpc.StatusCode.UNAUTHENTICATED, "missing or invalid token")
        with self.store.lock:
            sec = self.store.secrets.get(request.path)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, f"secret {request.path} not found")
            md = dict(context.invocation_metadata())
            if sec["token"]:
                if md.get("x-kms-secret-token") != sec["token"]:
                    context.abort(grpc.StatusCode.PERMISSION_DENIED, "secret token required")
            version = request.version or len(sec["versions"])
            if version < 1 or version > len(sec["versions"]):
                context.abort(grpc.StatusCode.NOT_FOUND, "version not found")
            value, ct = sec["versions"][version - 1]
        return kms_pb2.GetSecretResponse(
            path=request.path, version=version, value=value, content_type=ct,
        )

    def PutSecret(self, request, context):
        with self.store.lock:
            sec = self.store.secrets.get(request.path)
            token = ""
            if sec is None:
                token = "tok-" + request.path.replace("/", "_") if request.generate_access_token else ""
                sec = {
                    "value": request.value, "content_type": request.content_type or "application/octet-stream",
                    "token": token, "client_bound": request.client_bound, "versions": [],
                }
                self.store.secrets[request.path] = sec
            else:
                if request.generate_access_token:
                    token = "tok2-" + request.path.replace("/", "_")
                    sec["token"] = token
            sec["versions"].append((request.value, request.content_type or sec["content_type"]))
            version = len(sec["versions"])
            rev = self.store._next_rev()
        self.store._broadcast(kms_pb2.SubscribeEvent(
            secret_change=kms_pb2.SecretMetadataChange(path=request.path, change_type="put", version=version),
            revision=rev,
        ))
        return kms_pb2.PutSecretResponse(version=version, revision=rev, access_token=token)

    def GetSecretMetadata(self, request, context):
        with self.store.lock:
            sec = self.store.secrets.get(request.path)
            if sec is None:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            versions = [
                kms_pb2.SecretVersionInfo(version=i + 1, state="enabled")
                for i in range(len(sec["versions"]))
            ]
            meta = kms_pb2.SecretMetadata(
                path=request.path, content_type=sec["content_type"],
                client_bound=sec["client_bound"], has_access_token=bool(sec["token"]),
                labels={"current": len(sec["versions"])}, versions=versions,
            )
        return kms_pb2.GetSecretMetadataResponse(secret=meta)

    def DeleteSecret(self, request, context):
        with self.store.lock:
            if request.path not in self.store.secrets:
                context.abort(grpc.StatusCode.NOT_FOUND, "not found")
            del self.store.secrets[request.path]
            rev = self.store._next_rev()
        return kms_pb2.DeleteSecretResponse(revision=rev)


class WatchServicer(kms_pb2_grpc.WatchServiceServicer):
    def __init__(self, store: FakeStore) -> None:
        self.store = store

    def Subscribe(self, request_iterator, context):
        it = iter(request_iterator)
        reg = next(it)
        sub = _Subscription(paths=list(reg.paths), queue=[], cond=threading.Condition())

        with self.store.lock:
            self.store.subs.append(sub)
            rev = self.store.revision
            snap_params = []
            for p, versions in self.store.params.items():
                if any(_match(pat, p) for pat in sub.paths):
                    value, ct = versions[-1]
                    snap_params.append(kms_pb2.Parameter(
                        path=p, value=value, content_type=ct, version=len(versions),
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

    def WatchParameter(self, request, context):
        return iter(())

    def WatchNamespace(self, request, context):
        return iter(())


def start_server(*, require_bearer: Optional[str] = None) -> tuple:
    """Start the fake server on a random localhost port.

    Returns ``(server, address, store)``.
    """
    store = FakeStore(require_bearer=require_bearer)
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=16))
    kms_pb2_grpc.add_ParameterServiceServicer_to_server(ParameterServicer(store), server)
    kms_pb2_grpc.add_SecretServiceServicer_to_server(SecretServicer(store), server)
    kms_pb2_grpc.add_WatchServiceServicer_to_server(WatchServicer(store), server)
    port = server.add_insecure_port("localhost:0")
    server.start()
    return server, f"localhost:{port}", store
