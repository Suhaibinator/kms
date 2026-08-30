"""Asyncio-owned namespace watch lifecycle for :class:`AsyncClient`."""

from __future__ import annotations

import asyncio
import inspect
import random
import time
from typing import Awaitable, Callable, Dict, Optional, Set, Tuple, Union

from ._gen import kms_pb2
from ._refs import NamespaceRef, Ref
from .watch import Event, EventType, WatchStatus

AsyncWatchCallback = Callable[[Event], Union[object, Awaitable[object]]]
_NSKey = Tuple[str, str]
_RefKey = Tuple[str, str, str]


class AsyncSubscriptionManager:
    """One event-loop-bound, resumable watch stream shared by an async client."""

    def __init__(self, client, *, reconcile_interval: float = 300.0) -> None:
        self._client = client
        self._reconcile_interval = reconcile_interval
        self._namespaces: Set[_NSKey] = set()
        self._watchers: Dict[int, Tuple[NamespaceRef, AsyncWatchCallback]] = {}
        self._known: Dict[_RefKey, Tuple[str, bool, int]] = {}
        self._next_id = 0
        self._last_revision = 0
        self._state = "idle"
        self._reconciliation = "not_started"
        self._reconnect_count = 0
        self._connected_at: Optional[int] = None
        self._last_event_at: Optional[int] = None
        self._disconnected_at: Optional[int] = None
        self._last_reconcile_attempt_at: Optional[int] = None
        self._last_reconcile_success_at: Optional[int] = None
        self._last_reconcile_failure_at: Optional[int] = None
        self._run_task: Optional[asyncio.Task[None]] = None
        self._reconcile_task: Optional[asyncio.Task[None]] = None
        self._callback_queue: asyncio.Queue[Tuple[AsyncWatchCallback, Event]] = asyncio.Queue(1024)
        self._callback_task: Optional[asyncio.Task[None]] = None
        self._call = None
        self._restart = False
        self._closed = False

    @property
    def current_revision(self) -> int:
        return self._last_revision

    @property
    def status(self) -> WatchStatus:
        return WatchStatus(
            state=self._state,
            reconciliation=self._reconciliation,
            current_revision=self._last_revision,
            reconnect_count=self._reconnect_count,
            namespace_count=len(self._namespaces),
            tracked_parameter_count=len(self._known),
            watcher_count=len(self._watchers),
            parameter_handler_count=0,
            connected_at_unix_ms=self._connected_at,
            last_event_at_unix_ms=self._last_event_at,
            disconnected_at_unix_ms=self._disconnected_at,
            last_reconcile_attempt_at_unix_ms=self._last_reconcile_attempt_at,
            last_reconcile_success_at_unix_ms=self._last_reconcile_success_at,
            last_reconcile_failure_at_unix_ms=self._last_reconcile_failure_at,
        )

    def watch(self, namespace: NamespaceRef, callback: AsyncWatchCallback) -> Callable[[], None]:
        if not callable(callback):
            raise TypeError("watch callback is required")
        if self._closed:
            raise RuntimeError("watch manager is stopped")
        self._next_id += 1
        watcher_id = self._next_id
        self._watchers[watcher_id] = (namespace, callback)
        ns_key = (namespace.env, namespace.app)
        added = ns_key not in self._namespaces
        self._namespaces.add(ns_key)
        if self._run_task is None:
            self._run_task = asyncio.create_task(self._run(), name="kms-async-watch")
            self._reconcile_task = asyncio.create_task(
                self._reconcile_loop(), name="kms-async-reconcile"
            )
            self._callback_task = asyncio.create_task(
                self._dispatch_callbacks(), name="kms-async-callbacks"
            )
        elif added:
            self._restart = True
            if self._call is not None:
                self._call.cancel()
        active = True

        def stop() -> None:
            nonlocal active
            if active:
                active = False
                self._watchers.pop(watcher_id, None)

        return stop

    async def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        self._state = "stopped"
        if self._call is not None:
            self._call.cancel()
        tasks = [
            t for t in (self._run_task, self._reconcile_task, self._callback_task)
            if t is not None
        ]
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        self._watchers.clear()

    async def _run(self) -> None:
        attempt = 0
        while not self._closed:
            self._restart = False
            try:
                await self._run_stream()
            except asyncio.CancelledError:
                if self._closed:
                    return
            except Exception as exc:
                self._client._logf("async watch stream ended: %s", exc)
            if self._closed:
                return
            if self._restart:
                attempt = 0
                continue
            self._state = "reconnecting"
            self._reconnect_count += 1
            self._disconnected_at = _now_ms()
            delay = max(0.01, random.uniform(0, min(60.0, 2**attempt)))
            attempt += 1
            await asyncio.sleep(delay)

    async def _run_stream(self) -> None:
        namespaces = sorted(self._namespaces)
        self._state = "connecting" if self._reconnect_count == 0 else "reconnecting"
        call = self._client._watch_stub.Subscribe(metadata=self._client._auth_metadata())
        self._call = call
        await call.write(
            kms_pb2.SubscribeRequest(
                client_name=self._client._client_name,
                namespaces=[kms_pb2.NamespaceRef(env=e, app=a) for e, a in namespaces],
                last_seen_revision=self._last_revision,
            )
        )
        self._state = "connected"
        self._connected_at = _now_ms()
        try:
            async for event in call:
                await self._handle_event(event, set(namespaces), call)
        finally:
            self._call = None

    async def _handle_event(self, event, stream_namespaces: Set[_NSKey], call) -> None:
        self._last_event_at = _now_ms()
        revision = event.revision
        kind = event.WhichOneof("event")
        if kind == "snapshot":
            self._client._cache.invalidate_secrets_in_namespaces(stream_namespaces)
            present: Set[_RefKey] = set()
            for parameter in event.snapshot.parameters:
                rk = _proto_ref_key(parameter.ref)
                present.add(rk)
                self._apply_parameter(rk, parameter.value, True, parameter.version, revision)
            for rk, (_value, is_present, previous_revision) in tuple(self._known.items()):
                if is_present and rk not in present and (rk[0], rk[1]) in stream_namespaces:
                    self._apply_parameter(rk, "", False, 0, max(revision, previous_revision))
        elif kind == "change" and (revision == 0 or revision > self._last_revision):
            change = event.change
            rk = _proto_ref_key(change.ref)
            self._apply_parameter(
                rk, change.value, change.change_type != "delete", change.version, revision,
                change.change_type,
            )
        elif kind == "secret_change" and (revision == 0 or revision > self._last_revision):
            change = event.secret_change
            rk = _proto_ref_key(change.ref)
            self._client._cache.invalidate_secret(str(Ref(NamespaceRef(rk[0], rk[1]), rk[2])))
            self._emit(
                rk,
                Event(
                    EventType.SECRET_CHANGE, f"{rk[0]}/{rk[1]}", rk[2],
                    version=change.version, revision=revision, change_type=change.change_type,
                ),
            )
        if revision > self._last_revision:
            self._last_revision = revision
        if kind == "heartbeat":
            await call.write(kms_pb2.SubscribeRequest(acked_revision=self._last_revision))

    def _apply_parameter(
        self,
        rk: _RefKey,
        value: str,
        present: bool,
        version: int,
        revision: int,
        change_type: str = "",
    ) -> None:
        previous = self._known.get(rk)
        if previous is not None and revision and revision < previous[2]:
            return
        changed = previous is None or previous[0] != value or previous[1] != present
        self._known[rk] = (value if present else "", present, revision)
        self._client._cache.invalidate_param(str(Ref(NamespaceRef(rk[0], rk[1]), rk[2])))
        if changed:
            self._emit(
                rk,
                Event(
                    EventType.PUT if present else EventType.DELETE,
                    f"{rk[0]}/{rk[1]}", rk[2], value if present else "",
                    version, revision, change_type or ("put" if present else "delete"),
                ),
            )

    def _emit(self, rk: _RefKey, event: Event) -> None:
        for namespace, callback in tuple(self._watchers.values()):
            if (namespace.env, namespace.app) != (rk[0], rk[1]):
                continue
            try:
                self._callback_queue.put_nowait((callback, event))
            except asyncio.QueueFull:
                self._client._logf("async callback queue full, dropping notification for %s", event.path)

    async def _dispatch_callbacks(self) -> None:
        while True:
            callback, event = await self._callback_queue.get()
            await self._invoke(callback, event)

    async def _invoke(self, callback: AsyncWatchCallback, event: Event) -> None:
        try:
            result = callback(event)
            if inspect.isawaitable(result):
                await result
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            self._client._logf("async watch callback for %s raised: %s", event.path, exc)

    async def _reconcile_loop(self) -> None:
        while True:
            await asyncio.sleep(self._reconcile_interval)
            self._last_reconcile_attempt_at = _now_ms()
            failed = False
            for env, app in tuple(self._namespaces):
                token = ""
                for _ in range(1000):
                    try:
                        page = await self._client._list_parameters_ref(
                            NamespaceRef(env, app), page_token=token
                        )
                    except Exception:
                        failed = True
                        break
                    for parameter in page.items:
                        self._apply_parameter(
                            (parameter.env, parameter.app, parameter.key), parameter.value,
                            True, parameter.version, self._last_revision,
                        )
                    token = page.next_page_token
                    if not token:
                        break
                else:
                    failed = True
            if failed:
                self._reconciliation = "degraded"
                self._last_reconcile_failure_at = _now_ms()
            else:
                self._reconciliation = "healthy"
                self._last_reconcile_success_at = _now_ms()


def _proto_ref_key(ref) -> _RefKey:
    return ref.namespace.env, ref.namespace.app, ref.key


def _now_ms() -> int:
    return int(time.time() * 1000)
