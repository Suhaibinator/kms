"""Subscription stream management and hot reload.

``_SubManager`` owns the long-lived ``Subscribe`` bidi stream for a client:
connect, send registration, apply events, ack heartbeats, reconnect with
exponential backoff + jitter, resume by revision, and reconcile periodically.
Applications only ever see values and callbacks.
"""

from __future__ import annotations

import enum
import queue
import random
import threading
import time
from dataclasses import dataclass
from typing import TYPE_CHECKING, Callable, Dict, List, Optional

from ._gen import kms_pb2

if TYPE_CHECKING:
    from .client import Client

__all__ = ["Event", "EventType"]

# Safety-net full-sync poll cadence (plan 8.4.8).
_DEFAULT_RECONCILE_INTERVAL = 300.0  # seconds
_BACKOFF_BASE = 1.0
_BACKOFF_MAX = 60.0


class EventType(enum.Enum):
    PUT = "put"
    DELETE = "delete"
    SECRET_CHANGE = "secret_change"

    def __str__(self) -> str:
        return self.value


@dataclass
class Event:
    """Delivered to :meth:`Client.watch` callbacks when a watched path changes."""

    type: EventType
    path: str
    value: str = ""  # populated for PUT
    version: int = 0
    revision: int = 0
    change_type: str = ""  # raw server change_type, useful for secret changes


@dataclass
class _Watcher:
    id: int
    pattern: str
    fn: Callable[[Event], None]


@dataclass
class _Known:
    value: str
    present: bool


ParamHandler = Callable[[str, bool], None]  # (new_value, present)


def match_pattern(pattern: str, path: str) -> bool:
    """A pattern ending in ``*`` is a prefix match; otherwise exact."""
    if pattern.endswith("*"):
        return path.startswith(pattern[:-1])
    return pattern == path


def backoff_delay(attempt: int) -> float:
    """Exponential backoff with full jitter (base 1s, cap 60s)."""
    d = _BACKOFF_MAX
    if attempt < 6:
        shifted = _BACKOFF_BASE * (2 ** attempt)
        if shifted < _BACKOFF_MAX:
            d = shifted
    j = random.uniform(0, d)
    return max(j, 0.01)


class _SubManager:
    def __init__(self, client: "Client", reconcile_interval: float = _DEFAULT_RECONCILE_INTERVAL) -> None:
        self._client = client
        self._lock = threading.Lock()
        self._paths: set[str] = set()
        self._param_handlers: Dict[str, List[ParamHandler]] = {}
        self._known: Dict[str, _Known] = {}
        self._watchers: List[_Watcher] = []
        self._next_watcher_id = 0
        self._started = False
        self._last_rev = 0

        self._stop_event = threading.Event()
        self._restart_event = threading.Event()
        self._restart_requested = False
        self._reconcile_interval = reconcile_interval

        self._current_responses = None  # grpc call object, cancellable
        self._threads: List[threading.Thread] = []

    # --- registration ------------------------------------------------------

    def register_param(self, path: str, initial: str, handler: ParamHandler) -> None:
        with self._lock:
            new_path = self._add_path_locked(path)
            self._known[path] = _Known(initial, True)
            self._param_handlers.setdefault(path, []).append(handler)
            was_started = self._started
            self._ensure_started_locked()
        if was_started and new_path:
            self._signal_restart()

    def register_watcher(self, pattern: str, fn: Callable[[Event], None]) -> _Watcher:
        with self._lock:
            self._next_watcher_id += 1
            w = _Watcher(self._next_watcher_id, pattern, fn)
            self._watchers.append(w)
            new_path = self._add_path_locked(pattern)
            was_started = self._started
            self._ensure_started_locked()
        if was_started and new_path:
            self._signal_restart()
        return w

    def remove_watcher(self, w: _Watcher) -> None:
        with self._lock:
            try:
                self._watchers.remove(w)
            except ValueError:
                return
            still_needed = any(x.pattern == w.pattern for x in self._watchers)
            if w.pattern in self._param_handlers:
                still_needed = True
            if not still_needed:
                self._paths.discard(w.pattern)
            started = self._started
        if started and not still_needed:
            self._signal_restart()

    def _add_path_locked(self, p: str) -> bool:
        if p in self._paths:
            return False
        self._paths.add(p)
        return True

    def _ensure_started_locked(self) -> None:
        if self._started:
            return
        self._started = True
        run_t = threading.Thread(target=self._run, name="paramstore-watch", daemon=True)
        rec_t = threading.Thread(target=self._reconcile_loop, name="paramstore-reconcile", daemon=True)
        self._threads = [run_t, rec_t]
        run_t.start()
        rec_t.start()

    # --- revision bookkeeping ---------------------------------------------

    def _get_rev(self) -> int:
        with self._lock:
            return self._last_rev

    def _advance_rev(self, rev: int) -> None:
        with self._lock:
            if rev > self._last_rev:
                self._last_rev = rev

    def _should_apply(self, rev: int) -> bool:
        # Apply unversioned events, and versioned events strictly newer than the
        # last one seen (idempotent, at-least-once).
        return rev == 0 or rev > self._get_rev()

    def _snapshot_paths(self) -> List[str]:
        with self._lock:
            return sorted(self._paths)

    # --- lifecycle ---------------------------------------------------------

    def _signal_restart(self) -> None:
        self._restart_requested = True
        self._restart_event.set()
        resp = self._current_responses
        if resp is not None:
            try:
                resp.cancel()
            except Exception:
                pass

    def stop(self) -> None:
        self._stop_event.set()
        self._restart_event.set()
        resp = self._current_responses
        if resp is not None:
            try:
                resp.cancel()
            except Exception:
                pass
        for t in self._threads:
            t.join(timeout=2.0)

    def _run(self) -> None:
        attempt = 0
        while not self._stop_event.is_set():
            self._restart_requested = False
            self._restart_event.clear()
            err = self._run_stream()
            if self._stop_event.is_set():
                return
            if self._restart_requested:
                attempt = 0
                continue  # path set changed: reconnect immediately
            delay = backoff_delay(attempt)
            attempt += 1
            self._log("watch stream ended (%s); reconnecting in %.2fs", err, delay)
            # Interruptible sleep.
            if self._stop_event.wait(timeout=delay):
                return

    def _run_stream(self) -> Optional[Exception]:
        out_q: "queue.Queue[Optional[kms_pb2.SubscribeRequest]]" = queue.Queue()
        registration = kms_pb2.SubscribeRequest(
            client_name=self._client._client_name,
            paths=self._snapshot_paths(),
            last_seen_revision=self._get_rev(),
        )

        def request_iter():
            yield registration
            while True:
                item = out_q.get()
                if item is None:
                    return
                yield item

        try:
            responses = self._client._watch_stub.Subscribe(
                request_iter(), metadata=self._client._auth_metadata("")
            )
            self._current_responses = responses
            for ev in responses:
                self._handle_event(ev, out_q)
            return None
        except Exception as e:  # includes grpc.RpcError on cancel/disconnect
            return e
        finally:
            out_q.put(None)  # unblock the request iterator
            self._current_responses = None

    def _handle_event(self, ev, out_q) -> None:
        rev = ev.revision
        kind = ev.WhichOneof("event")
        if kind == "snapshot":
            self._apply_snapshot(ev.snapshot, rev)
            self._advance_rev(rev)
        elif kind == "change":
            if self._should_apply(rev):
                self._apply_change(ev.change, rev)
            self._advance_rev(rev)
        elif kind == "secret_change":
            if self._should_apply(rev):
                self._apply_secret_change(ev.secret_change, rev)
            self._advance_rev(rev)
        elif kind == "heartbeat":
            self._advance_rev(rev)
            out_q.put(kms_pb2.SubscribeRequest(acked_revision=self._get_rev()))

    def _apply_snapshot(self, snap, rev: int) -> None:
        for p in snap.parameters:
            self._set_value(p.path, p.value, True, p.version, rev)

    def _apply_change(self, change, rev: int) -> None:
        if change.change_type == "delete":
            self._set_value(change.path, "", False, change.version, rev)
        else:  # put | label
            self._set_value(change.path, change.value, True, change.version, rev)

    def _apply_secret_change(self, change, rev: int) -> None:
        self._client._cache.invalidate_secret(change.path)
        ev = Event(
            type=EventType.SECRET_CHANGE,
            path=change.path,
            version=change.version,
            revision=rev,
            change_type=change.change_type,
        )
        for w in self._matching_watchers(change.path):
            self._fire_watcher(w, ev)

    def _set_value(self, path: str, value: str, present: bool, version: int, rev: int) -> None:
        with self._lock:
            prev = self._known.get(path)
            if present:
                changed = prev is None or prev.value != value or not prev.present
                self._known[path] = _Known(value, True)
            else:
                changed = prev is not None and prev.present
                self._known.pop(path, None)
            handlers = list(self._param_handlers.get(path, ()))
            watchers = self._matching_watchers_locked(path)

        if not changed:
            return

        self._client._cache.invalidate_param(path)
        for h in handlers:
            try:
                h(value, present)
            except Exception as e:  # a bad handler must not stall the stream
                self._log("value handler for %s raised: %s", path, e)

        ev_type = EventType.PUT if present else EventType.DELETE
        ev = Event(type=ev_type, path=path, value=value, version=version, revision=rev)
        for w in watchers:
            self._fire_watcher(w, ev)

    def _matching_watchers(self, path: str) -> List[_Watcher]:
        with self._lock:
            return self._matching_watchers_locked(path)

    def _matching_watchers_locked(self, path: str) -> List[_Watcher]:
        return [w for w in self._watchers if match_pattern(w.pattern, path)]

    def _fire_watcher(self, w: _Watcher, ev: Event) -> None:
        fn = w.fn
        self._client._enqueue_callback(lambda: fn(ev))

    # --- reconcile ---------------------------------------------------------

    def _reconcile_loop(self) -> None:
        while not self._stop_event.wait(timeout=self._reconcile_interval):
            try:
                self._reconcile()
            except Exception as e:
                self._log("reconcile failed: %s", e)

    def _reconcile(self) -> None:
        with self._lock:
            param_paths = list(self._param_handlers.keys())
            prefixes = [w.pattern[:-1] for w in self._watchers if w.pattern.endswith("*")]

        for p in param_paths:
            try:
                val = self._client._fetch_parameter(p, version=0, label="", secret_token="")
            except Exception:
                continue  # keep last-known value
            self._set_value(p, val, True, 0, self._get_rev())

        for prefix in prefixes:
            self._reconcile_prefix(prefix)

    def _reconcile_prefix(self, prefix: str) -> None:
        page_token = ""
        for _ in range(100):  # bounded to avoid runaway loops
            try:
                resp = self._client._list_parameters_raw(prefix, page_token)
            except Exception:
                return
            for p in resp.parameters:
                self._set_value(p.path, p.value, True, p.version, self._get_rev())
            page_token = resp.next_page_token
            if not page_token:
                return

    def _log(self, fmt: str, *args) -> None:
        self._client._logf(fmt, *args)
