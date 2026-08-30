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
ParameterHandler = Callable[[str, bool], None]
_NSKey = Tuple[str, str]
_RefKey = Tuple[str, str, str]


class AsyncSubscriptionManager:
    """One event-loop-bound, resumable watch stream shared by an async client."""

    def __init__(self, client, *, reconcile_interval: float = 300.0) -> None:
        self._client = client
        self._reconcile_interval = reconcile_interval
        self._namespaces: Set[_NSKey] = set()
        self._watchers: Dict[int, Tuple[NamespaceRef, AsyncWatchCallback]] = {}
        self._parameter_handlers: Dict[_RefKey, Set[ParameterHandler]] = {}
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
        self._callback_queue: asyncio.Queue[Callable[[], Union[object, Awaitable[object]]]] = asyncio.Queue(1024)
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
            parameter_handler_count=sum(len(items) for items in self._parameter_handlers.values()),
            connected_at_unix_ms=self._connected_at,
            last_event_at_unix_ms=self._last_event_at,
            disconnected_at_unix_ms=self._disconnected_at,
            last_reconcile_attempt_at_unix_ms=self._last_reconcile_attempt_at,
            last_reconcile_success_at_unix_ms=self._last_reconcile_success_at,
            last_reconcile_failure_at_unix_ms=self._last_reconcile_failure_at,
        )

    def register_parameter(
        self, ref: Ref, initial: str, handler: ParameterHandler
    ) -> Callable[[], None]:
        rk = (ref.ns.env, ref.ns.app, ref.key)
        previous = self._known.get(rk)
        if previous is None:
            self._known[rk] = (initial, True, 0)
        else:
            handler(previous[0], previous[1])
        handlers = self._parameter_handlers.setdefault(rk, set())
        handlers.add(handler)
        ns_key = (ref.ns.env, ref.ns.app)
        added = ns_key not in self._namespaces
        self._namespaces.add(ns_key)
        self._ensure_started()
        if added and self._call is not None:
            self._restart = True
            self._call.cancel()
        active = True

        def stop() -> None:
            nonlocal active
            if not active:
                return
            active = False
            handlers.discard(handler)
            if not handlers:
                self._parameter_handlers.pop(rk, None)

        return stop

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
            self._ensure_started()
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

    def _ensure_started(self) -> None:
        if self._run_task is not None:
            return
        self._run_task = asyncio.create_task(self._run(), name="kms-async-watch")
        self._reconcile_task = asyncio.create_task(
            self._reconcile_loop(), name="kms-async-reconcile"
        )
        self._callback_task = asyncio.create_task(
            self._dispatch_callbacks(), name="kms-async-callbacks"
        )

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
        current = asyncio.current_task()
        tasks = [task for task in tasks if task is not current]
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
            self._client._cache.invalidate_parameters_in_namespaces(stream_namespaces)
            present: Set[_RefKey] = set()
            for parameter in event.snapshot.parameters:
                rk = _proto_ref_key(parameter.ref)
                present.add(rk)
                self._apply_parameter(
                    rk, parameter.value, True, parameter.version, revision, reconcile=True
                )
            for rk, (_value, is_present, _revision) in tuple(self._known.items()):
                if is_present and rk not in present and (rk[0], rk[1]) in stream_namespaces:
                    self._apply_parameter(
                        rk, "", False, 0, revision,
                        change_type="delete", reconcile=True,
                    )
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
        elif kind == "heartbeat":
            await call.write(kms_pb2.SubscribeRequest(acked_revision=max(self._last_revision, revision)))
        else:
            return
        if revision > self._last_revision:
            self._last_revision = revision

    def _apply_parameter(
        self,
        rk: _RefKey,
        value: str,
        present: bool,
        version: int,
        revision: int,
        change_type: str = "",
        reconcile: bool = False,
    ) -> None:
        previous = self._known.get(rk)
        if previous is not None:
            previous_revision = previous[2]
            if reconcile and previous_revision > revision:
                return
            if not reconcile and revision != 0 and revision <= previous_revision:
                return
        changed = previous is None or previous[0] != value or previous[1] != present
        new_revision = max(revision, previous[2] if previous is not None else 0)
        self._known[rk] = (value if present else "", present, new_revision)
        self._client._cache.invalidate_param(str(Ref(NamespaceRef(rk[0], rk[1]), rk[2])))
        if not changed:
            return
        for handler in tuple(self._parameter_handlers.get(rk, ())):
            try:
                handler(value, present)
            except Exception as exc:
                self._client._logf("async value handler for %s raised: %s", rk[2], exc)
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
                def invoke(
                    callback: AsyncWatchCallback = callback, event: Event = event
                ) -> Union[object, Awaitable[object]]:
                    return callback(event)

                self.dispatch(invoke)
            except asyncio.QueueFull:
                self._client._logf("async callback queue full, dropping notification for %s", event.path)

    def dispatch(self, callback: Callable[[], Union[object, Awaitable[object]]]) -> None:
        self._callback_queue.put_nowait(callback)

    async def _dispatch_callbacks(self) -> None:
        while True:
            callback = await self._callback_queue.get()
            try:
                result = callback()
                if inspect.isawaitable(result):
                    await result
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                self._client._logf("async callback raised: %s", exc)
            if self._closed:
                return

    async def _reconcile_loop(self) -> None:
        while True:
            await asyncio.sleep(self._reconcile_interval)
            await self._reconcile_once()

    async def _reconcile_once(self) -> None:
        self._last_reconcile_attempt_at = _now_ms()
        failed = False
        snapshot_revision = self._last_revision
        for env, app in tuple(self._namespaces):
            token = ""
            present: Set[_RefKey] = set()
            page_failed = False
            for _ in range(1000):
                try:
                    page = await self._client._list_parameters_ref(
                        NamespaceRef(env, app), page_token=token
                    )
                except Exception:
                    failed = True
                    page_failed = True
                    break
                for parameter in page.items:
                    rk = (parameter.env, parameter.app, parameter.key)
                    present.add(rk)
                    self._apply_parameter(
                        rk, parameter.value, True, parameter.version,
                        snapshot_revision, reconcile=True,
                    )
                token = page.next_page_token
                if not token:
                    break
            else:
                failed = True
                page_failed = True
            if not page_failed:
                for rk, (_value, is_present, _revision) in tuple(self._known.items()):
                    if is_present and rk[:2] == (env, app) and rk not in present:
                        self._apply_parameter(
                            rk, "", False, 0, snapshot_revision,
                            change_type="delete", reconcile=True,
                        )
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
