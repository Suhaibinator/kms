"""Subscription stream management and hot reload.

``_SubManager`` owns the long-lived ``Subscribe`` bidi stream for a client:
connect, send registration, apply events, ack heartbeats, reconnect with
exponential backoff + jitter, resume by revision, and reconcile periodically.
Applications only ever see values and callbacks.

Interest is expressed as namespace-scoped *selectors* ``(env, app, key_pattern)``.
Hot-reloading parameters register through one namespace-wide selector
(``{ns, "*"}``) so every non-static field in a namespace rides a single stream
registration; explicit :meth:`Client.watch` calls add their own selectors.
"""

from __future__ import annotations

import enum
import queue
import random
import threading
import time
from dataclasses import dataclass
from typing import TYPE_CHECKING, Callable, Dict, List, Optional, Set, Tuple

from . import errors
from ._gen import kms_pb2
from ._refs import NamespaceRef, Ref, match_key

if TYPE_CHECKING:
    from .client import Client

__all__ = ["Event", "EventType"]

# Safety-net full-sync poll cadence (plan 8.4.8).
_DEFAULT_RECONCILE_INTERVAL = 300.0  # seconds
_BACKOFF_BASE = 1.0
_BACKOFF_MAX = 60.0

# (env, app, key) identifies a resource; (env, app, key_pattern) a selector.
_RefKey = Tuple[str, str, str]
_Selector = Tuple[str, str, str]


class EventType(enum.Enum):
    PUT = "put"
    DELETE = "delete"
    SECRET_CHANGE = "secret_change"

    def __str__(self) -> str:
        return self.value


@dataclass
class Event:
    """Delivered to :meth:`Client.watch` callbacks when a watched key changes."""

    type: EventType
    namespace: str  # "env/app"
    key: str        # relative key within the namespace
    value: str = ""  # populated for PUT
    version: int = 0
    revision: int = 0
    change_type: str = ""  # raw server change_type, useful for secret changes

    @property
    def path(self) -> str:
        """Absolute display path ``/env/app/key`` for logging."""
        if self.namespace:
            return f"/{self.namespace}/{self.key}"
        return self.key


@dataclass
class _Watcher:
    id: int
    ns: NamespaceRef
    key_pattern: str
    fn: Callable[[Event], None]


@dataclass
class _Known:
    value: str
    present: bool
    # Revision the value was last applied at. Fences stale writes: a
    # reconcile/reconnect read that raced a newer live event is dropped.
    rev: int = 0


ParamHandler = Callable[[str, bool], None]  # (new_value, present)


def _rev_allows_write(prev_rev: int, rev: int, reconcile: bool) -> bool:
    """Stale-write fence for :meth:`_SubManager._set_value`.

    A live event (``reconcile=False``) must be strictly newer than what was
    already applied (``rev == 0`` is unversioned/best-effort and always
    applies). A reconcile read (``reconcile=True``) carries the snapshot
    revision captured before the fetch, and is authoritative only if no newer
    live event has advanced the key past it.
    """
    if reconcile:
        return prev_rev <= rev
    return rev == 0 or rev > prev_rev


def backoff_delay(attempt: int) -> float:
    """Exponential backoff with full jitter (base 1s, cap 60s)."""
    d = _BACKOFF_MAX
    if attempt < 6:
        shifted = _BACKOFF_BASE * (2 ** attempt)
        if shifted < _BACKOFF_MAX:
            d = shifted
    j = random.uniform(0, d)
    return max(j, 0.01)


def _ref_key(ref: Ref) -> _RefKey:
    return (ref.ns.env, ref.ns.app, ref.key)


def _proto_ref_key(pref) -> _RefKey:
    return (pref.namespace.env, pref.namespace.app, pref.key)


def _pattern_prefix(pattern: str) -> str:
    """Server list KeyPrefix implied by a watch key pattern.

    The server's list prefix is key-level: prefix ``"billing"`` matches the base
    key ``"billing"`` and everything under ``"billing/"``. So ``"billing/*"`` maps
    to ``"billing"`` (not ``"billing/"``, which matches nothing), and a
    whole-namespace ``"*"``/``""`` pattern maps to an empty prefix.
    """
    if pattern in ("*", ""):
        return ""
    if pattern.endswith("/*"):
        return pattern[:-2]
    return pattern


class _SubManager:
    def __init__(self, client: "Client", reconcile_interval: float = _DEFAULT_RECONCILE_INTERVAL) -> None:
        self._client = client
        self._lock = threading.Lock()
        self._selectors: Set[_Selector] = set()
        # Selectors actually sent on the current stream's Subscribe request.
        # A snapshot is authoritative only for these, so deletions are diffed
        # against this scope (not the live selector set, which may have grown
        # since the request was sent).
        self._stream_selectors: List[_Selector] = []
        self._param_handlers: Dict[_RefKey, List[ParamHandler]] = {}
        self._known: Dict[_RefKey, _Known] = {}
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

    def register_param(self, ref: Ref, initial: str, handler: ParamHandler) -> None:
        """Register a hot-reloading parameter on the namespace-wide selector."""
        with self._lock:
            selector = (ref.ns.env, ref.ns.app, "*")
            new_selector = self._add_selector_locked(selector)
            rk = _ref_key(ref)
            self._known[rk] = _Known(initial, True)
            self._param_handlers.setdefault(rk, []).append(handler)
            was_started = self._started
            self._ensure_started_locked()
        if was_started and new_selector:
            self._signal_restart()

    def register_watcher(self, ns: NamespaceRef, key_pattern: str, fn: Callable[[Event], None]) -> _Watcher:
        with self._lock:
            self._next_watcher_id += 1
            w = _Watcher(self._next_watcher_id, ns, key_pattern, fn)
            self._watchers.append(w)
            new_selector = self._add_selector_locked((ns.env, ns.app, key_pattern))
            was_started = self._started
            self._ensure_started_locked()
        if was_started and new_selector:
            self._signal_restart()
        return w

    def remove_watcher(self, w: _Watcher) -> None:
        with self._lock:
            try:
                self._watchers.remove(w)
            except ValueError:
                return
            selector = (w.ns.env, w.ns.app, w.key_pattern)
            still_needed = self._selector_needed_locked(selector)
            if not still_needed:
                self._selectors.discard(selector)
            started = self._started
        if started and not still_needed:
            self._signal_restart()

    def _selector_needed_locked(self, selector: _Selector) -> bool:
        env, app, pattern = selector
        if any((w.ns.env, w.ns.app, w.key_pattern) == selector for w in self._watchers):
            return True
        # Hot-reloading parameters hold the namespace-wide "*" selector.
        if pattern == "*" and any(rk[0] == env and rk[1] == app for rk in self._param_handlers):
            return True
        return False

    def _add_selector_locked(self, selector: _Selector) -> bool:
        if selector in self._selectors:
            return False
        self._selectors.add(selector)
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

    def _snapshot_selectors(self) -> List[_Selector]:
        with self._lock:
            return sorted(self._selectors)

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
                continue  # selector set changed: reconnect immediately
            delay = backoff_delay(attempt)
            attempt += 1
            self._log("watch stream ended (%s); reconnecting in %.2fs", err, delay)
            # Interruptible sleep.
            if self._stop_event.wait(timeout=delay):
                return

    def _run_stream(self) -> Optional[Exception]:
        out_q: "queue.Queue[Optional[kms_pb2.SubscribeRequest]]" = queue.Queue()
        sels = self._snapshot_selectors()
        with self._lock:
            self._stream_selectors = list(sels)
        registration = kms_pb2.SubscribeRequest(
            client_name=self._client._client_name,
            selectors=[
                kms_pb2.WatchSelector(namespace=kms_pb2.NamespaceRef(env=env, app=app), key_pattern=pat)
                for (env, app, pat) in sels
            ],
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
        # A snapshot is authoritative for this stream's selectors: it enumerates
        # every currently-present parameter under them. Apply the present values,
        # then treat any previously-known key in scope but absent from the
        # snapshot as deleted — a parameter removed while we were disconnected
        # past the replay window. Keys outside the subscribed scope are untouched.
        present: Set[_RefKey] = set()
        for p in snap.parameters:
            rk = _proto_ref_key(p.ref)
            present.add(rk)
            self._set_value(rk, p.value, True, p.version, rev)
        for rk in self._absent_known_keys(present):
            self._set_value(rk, "", False, 0, rev)

    def _absent_known_keys(self, present: Set[_RefKey]) -> List[_RefKey]:
        """Known-present keys within the current stream's scope missing from the snapshot."""
        with self._lock:
            out: List[_RefKey] = []
            for rk, known in self._known.items():
                if not known.present:
                    continue
                if rk in present:
                    continue
                if not self._key_in_scope_locked(rk):
                    continue
                out.append(rk)
            return out

    def _key_in_scope_locked(self, rk: _RefKey) -> bool:
        env, app, key = rk
        for (senv, sapp, pat) in self._stream_selectors:
            if senv == env and sapp == app and match_key(pat, key):
                return True
        return False

    def _apply_change(self, change, rev: int) -> None:
        rk = _proto_ref_key(change.ref)
        if change.change_type == "delete":
            self._set_value(rk, "", False, change.version, rev)
        else:  # put | label
            self._set_value(rk, change.value, True, change.version, rev)

    def _apply_secret_change(self, change, rev: int) -> None:
        rk = _proto_ref_key(change.ref)
        env, app, key = rk
        self._client._cache.invalidate_secret(str(Ref(NamespaceRef(env, app), key)))
        ev = Event(
            type=EventType.SECRET_CHANGE,
            namespace=f"{env}/{app}",
            key=key,
            version=change.version,
            revision=rev,
            change_type=change.change_type,
        )
        for w in self._matching_watchers(rk):
            self._fire_watcher(w, ev)

    def _set_value(
        self, rk: _RefKey, value: str, present: bool, version: int, rev: int, reconcile: bool = False
    ) -> None:
        with self._lock:
            prev = self._known.get(rk)
            if prev is not None and not _rev_allows_write(prev.rev, rev, reconcile):
                return  # stale write: a newer revision already applied for this key
            new_rev = rev
            if prev is not None and prev.rev > new_rev:
                new_rev = prev.rev
            if present:
                changed = prev is None or prev.value != value or not prev.present
                self._known[rk] = _Known(value, True, new_rev)
            else:
                changed = prev is not None and prev.present
                self._known.pop(rk, None)
            handlers = list(self._param_handlers.get(rk, ()))
            watchers = self._matching_watchers_locked(rk)

        if not changed:
            return

        env, app, key = rk
        self._client._cache.invalidate_param(str(Ref(NamespaceRef(env, app), key)))
        for h in handlers:
            try:
                h(value, present)
            except Exception as e:  # a bad handler must not stall the stream
                self._log("value handler for %s/%s raised: %s", f"{env}/{app}", key, e)

        ev_type = EventType.PUT if present else EventType.DELETE
        ev = Event(
            type=ev_type, namespace=f"{env}/{app}", key=key, value=value, version=version, revision=rev
        )
        for w in watchers:
            self._fire_watcher(w, ev)

    def _matching_watchers(self, rk: _RefKey) -> List[_Watcher]:
        with self._lock:
            return self._matching_watchers_locked(rk)

    def _matching_watchers_locked(self, rk: _RefKey) -> List[_Watcher]:
        env, app, key = rk
        return [
            w for w in self._watchers
            if w.ns.env == env and w.ns.app == app and match_key(w.key_pattern, key)
        ]

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
            param_refs = list(self._param_handlers.keys())
            prefixes = [
                (w.ns, _pattern_prefix(w.key_pattern))
                for w in self._watchers
                if w.key_pattern.endswith("*")
            ]

        # Capture the snapshot revision before any read. Every value fetched
        # reflects the store at least as of this revision; a live event that
        # lands with a higher revision while we read wins over these (now stale)
        # reads, enforced by reconcile=True in _set_value.
        snap_rev = self._get_rev()

        for rk in param_refs:
            env, app, key = rk
            ref = Ref(NamespaceRef(env, app), key)
            try:
                val = self._client._fetch_parameter(ref, version=0, label="", secret_token="")
            except errors.NotFoundError:
                # A hot-reloading parameter the store no longer has was deleted
                # while the stream missed the event: treat it as a deletion so
                # the field reverts to its default.
                self._set_value(rk, "", False, 0, snap_rev, reconcile=True)
                continue
            except Exception:
                continue  # other errors (unavailable, timeout) keep last-known value
            self._set_value(rk, val, True, 0, snap_rev, reconcile=True)

        for ns, key_prefix in prefixes:
            self._reconcile_prefix(ns, key_prefix, snap_rev)

    def _reconcile_prefix(self, ns: NamespaceRef, key_prefix: str, snap_rev: int) -> None:
        page_token = ""
        for _ in range(100):  # bounded to avoid runaway loops
            try:
                resp = self._client._list_parameters_raw(ns, key_prefix, page_token)
            except Exception:
                return
            for p in resp.parameters:
                self._set_value(_proto_ref_key(p.ref), p.value, True, p.version, snap_rev, reconcile=True)
            page_token = resp.next_page_token
            if not page_token:
                return

    def _log(self, fmt: str, *args) -> None:
        self._client._logf(fmt, *args)
