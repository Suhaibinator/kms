"""Atomic configuration release loading and hot reload.

The release loader deliberately keeps decoding outside the SDK.  It resolves
an immutable server manifest, verifies every exact version and parameter
digest, and hands the application a complete snapshot to prepare.  The
application's commit step is the only mutation and must be infallible.
"""

from __future__ import annotations

import concurrent.futures
import hashlib
import hmac
import math
import random
import threading
import time
import uuid
from dataclasses import dataclass, field, replace
from types import MappingProxyType
from typing import (
    TYPE_CHECKING,
    Callable,
    Dict,
    Mapping,
    Optional,
    Protocol,
    Tuple,
    TypeVar,
    Union,
)

import grpc

from . import errors
from ._gen import kms_pb2, kms_pb2_grpc
from ._refs import NamespaceRef, Ref, split_display_path, to_proto_namespace, to_proto_ref
from .secret import Secret

if TYPE_CHECKING:
    from .client import Client

__all__ = [
    "ClassifiedReleaseError",
    "PreparedRelease",
    "ReleaseCandidateError",
    "ReleaseCommitError",
    "ReleaseEntry",
    "ReleaseDivergenceReporter",
    "ReleaseManifest",
    "ReleaseLoader",
    "ReleaseLoaderConfig",
    "ReleaseLoaderError",
    "ReleaseSnapshot",
    "ReleaseStartupError",
    "ReleaseStats",
    "ReleaseStatus",
    "SecretTokenProvider",
    "RELEASE_REJECTION_CATEGORIES",
    "RELEASE_STATES",
    "run_typed_release",
]

_KIND_PARAMETER = "parameter"
_KIND_SECRET = "secret"
RELEASE_STATES = ("received", "prepared", "applied", "rejected")
RELEASE_REJECTION_CATEGORIES = (
    "resolution_failed",
    "token_unavailable",
    "version_mismatch",
    "digest_mismatch",
    "prepare_failed",
    "config_contract_mismatch",
    "config_decode_failed",
    "config_validation_failed",
    "default_mismatch",
    "restart_required",
    "superseded",
    "active_check_failed",
    "internal",
)
_STATES = RELEASE_STATES
_REJECTION_CATEGORIES = frozenset(RELEASE_REJECTION_CATEGORIES)
_DIAGNOSTIC_LIMIT = 128
_MAX_DIVERGENT_FIELD_COUNT = 65_535


def _validate_release_timing(name: str, value: Optional[float]) -> None:
    if value is None:
        return
    if (
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not math.isfinite(value)
        or value <= 0
    ):
        raise errors.ConfigError(f"release {name} must be finite and positive")


class PreparedRelease(Protocol):
    """Application resources prepared for an atomic configuration swap."""

    def commit(self) -> None:
        """Atomically install the prepared state. This must be infallible."""

    def abort(self) -> None:
        """Release resources when this candidate will not be committed."""


class ReleaseDivergenceReporter(Protocol):
    """Optional prepared-release capability for value-free drift reporting."""

    def release_divergence(self) -> Tuple[bool, int]:
        """Report whether source defaults differ and the bounded field count."""


SecretTokenResult = Union[str, Tuple[str, bool], None]
SecretTokenProvider = Callable[[str, str], SecretTokenResult]


class ReleaseLoaderError(errors.ParamStoreError):
    """Base error for release-loader lifecycle failures."""


class ReleaseStartupError(ReleaseLoaderError):
    """The initial active release could not be prepared and applied."""


class ReleaseCommitError(ReleaseLoaderError):
    """Application ``commit`` raised, violating the prepared-release contract."""


class ClassifiedReleaseError(ReleaseLoaderError):
    """Classify a local validation failure without exposing its message remotely."""

    def __init__(self, category: str, message: str = "") -> None:
        if category not in _REJECTION_CATEGORIES:
            raise ValueError("invalid release rejection category")
        super().__init__(message or f"configuration release rejected ({category})")
        self.release_rejection_category = category


class ReleaseCandidateError(ReleaseStartupError):
    """Redacted error for an initial candidate that could not be applied."""

    def __init__(self, category: str) -> None:
        self.category = category if category in _REJECTION_CATEGORIES else "internal"
        super().__init__(f"initial configuration release was rejected ({self.category})")


@dataclass(frozen=True)
class ReleaseEntry:
    """One exact immutable resource reference in a release."""

    alias: str
    kind: str
    path: str
    version: int
    content_type: str
    metadata_json: str
    parameter_digest: str = ""


@dataclass(frozen=True)
class ReleaseManifest:
    """Immutable unresolved manifest passed to validation before any fetch."""

    namespace: str
    name: str
    version: int
    activation_revision: int
    schema_version: int
    digest: str
    metadata_json: str
    entries: Mapping[str, ReleaseEntry]

    def entry(self, alias: str) -> Optional[ReleaseEntry]:
        return self.entries.get(alias)

    def __repr__(self) -> str:
        return (
            "ReleaseManifest("
            f"namespace={self.namespace!r}, name={self.name!r}, "
            f"version={self.version}, activation_revision={self.activation_revision}, "
            f"digest={self.digest!r}, entries={len(self.entries)})"
        )

    __str__ = __repr__

    def __format__(self, _spec: str) -> str:
        return self.__repr__()


@dataclass(frozen=True)
class ReleaseSnapshot:
    """A complete immutable candidate. Secret formatting always redacts."""

    namespace: str
    name: str
    version: int
    activation_revision: int
    schema_version: int
    digest: str
    metadata_json: str
    entries: Tuple[ReleaseEntry, ...]
    parameters: Mapping[str, str]
    secrets: Mapping[str, Secret]

    def __repr__(self) -> str:
        # Keep resolved values out of routine diagnostics. Parameter documents
        # are not secrets, but excluding both maps mirrors the Go SDK and makes
        # accidental logging of a complete application configuration unlikely.
        return (
            "ReleaseSnapshot("
            f"namespace={self.namespace!r}, name={self.name!r}, "
            f"version={self.version}, activation_revision={self.activation_revision}, "
            f"digest={self.digest!r}, entries={len(self.entries)}, "
            f"parameters={len(self.parameters)}, secrets={len(self.secrets)} [REDACTED])"
        )

    __str__ = __repr__

    def __format__(self, _spec: str) -> str:
        return self.__repr__()


@dataclass(frozen=True)
class ReleaseStatus:
    """A bounded, non-sensitive view of loader state."""

    state: str = "idle"
    observed_version: int = 0
    observed_revision: int = 0
    applied_version: int = 0
    applied_revision: int = 0
    last_failure_category: str = ""
    last_failure_unix_ms: int = 0
    last_resolution_duration_ms: int = 0
    reconnects: int = 0

    @property
    def active_version(self) -> int:
        """The newest release version observed from watch/reconciliation."""
        return self.observed_version

    @property
    def active_revision(self) -> int:
        """The activation revision of :attr:`active_version`."""
        return self.observed_revision


@dataclass(frozen=True)
class ReleaseStats:
    """Low-cardinality counters and timing for a loader instance."""

    candidates: int = 0
    applied: int = 0
    rejected: Mapping[str, int] = field(default_factory=lambda: MappingProxyType({}))
    reconnects: int = 0
    # Compatibility observability retained for v0.1 callers. New integrations
    # should use candidates/applied/rejected/reconnects, matching Go and TS.
    resolutions: int = 0
    resolution_failures: int = 0
    last_resolution_ms: int = 0
    acknowledgements: Mapping[str, int] = field(default_factory=lambda: MappingProxyType({}))

    def __post_init__(self) -> None:
        object.__setattr__(self, "rejected", MappingProxyType(dict(self.rejected)))
        object.__setattr__(self, "acknowledgements", MappingProxyType(dict(self.acknowledgements)))

    @property
    def rejections(self) -> Mapping[str, int]:
        """Deprecated alias for :attr:`rejected`."""
        return self.rejected


@dataclass(frozen=True)
class ReleaseLoaderConfig:
    """Release loader settings.

    ``instance_id`` is normally omitted; the loader generates it once and
    reuses it across all stream reconnects. A caller may provide a stable ID
    when process identity is managed externally.
    """

    name: str
    namespace: "Optional[str | NamespaceRef]" = None
    reconcile_interval: float = 60.0
    secret_token_provider: Optional[SecretTokenProvider] = None
    binding_keys: Mapping[str, str] = field(
        default_factory=lambda: MappingProxyType({}), repr=False, compare=False
    )
    validate_manifest: Optional[Callable[[threading.Event, ReleaseManifest], None]] = None
    max_concurrent_fetches: int = 16
    client_name: Optional[str] = None
    instance_id: Optional[str] = None
    reconnect_initial: float = 0.25
    reconnect_max: float = 30.0
    request_timeout: Optional[float] = None

    def __post_init__(self) -> None:
        object.__setattr__(self, "binding_keys", MappingProxyType(dict(self.binding_keys)))


@dataclass(frozen=True)
class _Candidate:
    release: kms_pb2.ConfigurationRelease
    revision: int

    @property
    def identity(self) -> Tuple[int, int, str, str]:
        return (
            self.revision,
            self.release.version,
            self.release.name,
            self.release.digest,
        )  # type: ignore[attr-defined]


class _CandidateFailure(Exception):
    def __init__(self, category: str, diagnostic: str = "") -> None:
        super().__init__(category)
        self.category = category if category in _REJECTION_CATEGORIES else "internal"
        self.diagnostic = diagnostic[:_DIAGNOSTIC_LIMIT]


class ReleaseLoader:
    """Reliably resolve, prepare, and atomically apply one named release.

    :meth:`run` blocks until stopped and permits one active invocation at a
    time; the loader may be reused sequentially after a run exits. Public
    status/statistics methods are safe to call concurrently. Release events
    use a dedicated stream and a replace-latest candidate slot, never the
    client's best-effort callback queue.
    """

    def __init__(self, client: "Client", config: ReleaseLoaderConfig) -> None:
        if not config.name:
            raise errors.ConfigError("release name is required")
        if config.max_concurrent_fetches < 1 or config.max_concurrent_fetches > 256:
            raise errors.ConfigError(
                "release max_concurrent_fetches must be between 1 and 256"
            )
        _validate_release_timing("reconcile_interval", config.reconcile_interval)
        _validate_release_timing("reconnect_initial", config.reconnect_initial)
        _validate_release_timing("reconnect_max", config.reconnect_max)
        _validate_release_timing("request_timeout", config.request_timeout)
        if config.reconnect_max < config.reconnect_initial:
            raise errors.ConfigError("invalid release reconnect backoff")

        self._client = client
        self._config = config
        self._binding_keys = MappingProxyType(dict(config.binding_keys))
        self._namespace = client._resolve_namespace_arg(config.namespace)
        self._client_name = config.client_name or client._client_name
        self._instance_id = config.instance_id or str(uuid.uuid4())
        self._stub = kms_pb2_grpc.ConfigurationReleaseServiceStub(client._channel)

        self._run_lock = threading.Lock()
        self._running = False
        self._run_generation = 0
        self._stop_event = threading.Event()
        self._watch_thread: Optional[threading.Thread] = None
        self._relay_thread: Optional[threading.Thread] = None
        self._relay_done = threading.Event()
        self._executor: Optional[concurrent.futures.ThreadPoolExecutor] = None

        self._candidate_cond = threading.Condition()
        self._pending_candidate: Optional[_Candidate] = None
        self._active_cancel: Optional[threading.Event] = None
        self._active_identity: Optional[Tuple[int, int, str, str]] = None
        self._latest_identity: Optional[Tuple[int, int, str, str]] = None
        self._retry_identity: Optional[Tuple[int, int, str, str]] = None
        self._last_seen_revision = 0

        self._ack_cond = threading.Condition()
        self._ack_sequence = 0
        self._ack_flushed_by_state: Dict[str, int] = {}
        self._ack_latest: Dict[str, Tuple[int, kms_pb2.ReleaseAcknowledgement]] = {}

        self._graceful_watch_stop = threading.Event()
        self._watch_done = threading.Event()

        self._call_lock = threading.Lock()
        self._watch_call = None

        self._state_lock = threading.Lock()
        self._status = ReleaseStatus()
        self._reconnects = 0
        self._resolutions = 0
        self._resolution_failures = 0
        self._last_resolution_ms = 0
        self._candidates = 0
        self._applied = 0
        self._ack_counts: Dict[str, int] = {state: 0 for state in _STATES}
        self._rejection_counts: Dict[str, int] = {
            category: 0 for category in sorted(_REJECTION_CATEGORIES)
        }

    @property
    def instance_id(self) -> str:
        return self._instance_id

    @property
    def client_name(self) -> str:
        return self._client_name

    def status(self) -> ReleaseStatus:
        with self._state_lock:
            return self._status

    def stats(self) -> ReleaseStats:
        with self._state_lock:
            return ReleaseStats(
                candidates=self._candidates,
                applied=self._applied,
                rejected=self._rejection_counts,
                reconnects=self._reconnects,
                resolutions=self._resolutions,
                resolution_failures=self._resolution_failures,
                last_resolution_ms=self._last_resolution_ms,
                acknowledgements=self._ack_counts,
            )

    def stop(self) -> None:
        """Stop a running loader and cancel its current candidate/stream."""
        self._stop_event.set()
        with self._candidate_cond:
            if self._active_cancel is not None:
                self._active_cancel.set()
            self._candidate_cond.notify_all()
        with self._ack_cond:
            self._ack_cond.notify_all()
        with self._call_lock:
            call = self._watch_call
        if call is not None:
            try:
                call.cancel()
            except Exception:
                pass

    def run(
        self,
        prepare: Callable[[threading.Event, ReleaseSnapshot], PreparedRelease],
        *,
        stop_event: Optional[threading.Event] = None,
    ) -> None:
        """Apply the initial release, then block and hot reload until stopped.

        ``prepare`` receives a cancellation event that is set when a newer
        activation supersedes its candidate. It must return an object whose
        ``commit`` is infallible and whose ``abort`` releases uncommitted work.
        """
        if not callable(prepare):
            raise errors.ConfigError("release prepare callback is required")
        with self._run_lock:
            if self._running:
                raise errors.ConfigError("a ReleaseLoader is already running")
            self._running = True
            self._run_generation += 1
            run_generation = self._run_generation
            self._stop_event = threading.Event()
            self._pending_candidate = None
            self._active_cancel = None
            self._active_identity = None
            self._latest_identity = None
            self._retry_identity = None
            self._graceful_watch_stop = threading.Event()
            self._watch_done = threading.Event()
            self._relay_done = threading.Event()
            self._relay_thread = None

        self._executor = concurrent.futures.ThreadPoolExecutor(
            max_workers=self._config.max_concurrent_fetches,
            thread_name_prefix="kms-release-resolve",
        )
        try:
            initial = self._read_active()
        except Exception:
            with self._run_lock:
                self._running = False
            self._executor.shutdown(wait=True, cancel_futures=True)
            self._executor = None
            raise ReleaseStartupError(
                "unable to read the initial active configuration release"
            ) from None
        if not initial.release.name:  # type: ignore[attr-defined]
            with self._run_lock:
                self._running = False
            self._executor.shutdown(wait=True, cancel_futures=True)
            self._executor = None
            raise ReleaseStartupError("active configuration release response was empty")
        with self._candidate_cond:
            self._last_seen_revision = initial.revision
        self._watch_thread = threading.Thread(
            target=self._watch_loop, name="kms-release-watch", daemon=True
        )
        self._watch_thread.start()
        if stop_event is not None:
            def relay_stop() -> None:
                while not stop_event.is_set():
                    if self._relay_done.wait(0.05):
                        return
                with self._run_lock:
                    active = self._running and self._run_generation == run_generation
                if active:
                    self.stop()

            self._relay_thread = threading.Thread(
                target=relay_stop, name="kms-release-stop", daemon=True
            )
            self._relay_thread.start()

        applied_once = False
        next_reconcile = time.monotonic() + self._config.reconcile_interval
        try:
            self._offer_candidate(initial, source="reconciliation")

            while not self._stop_event.is_set():
                wait_for = min(0.25, max(0.0, next_reconcile - time.monotonic()))
                candidate = self._take_candidate(wait_for)
                if candidate is not None:
                    outcome, category, acknowledgement_generation = self._process_candidate(
                        candidate, prepare
                    )
                    self._record_retry_eligibility(candidate, outcome)
                    if outcome == "applied":
                        applied_once = True
                    elif outcome == "rejected" and not applied_once:
                        # An activation may have superseded a rejected startup
                        # candidate before cancellation became observable. Give
                        # the fresh active identity one chance to replace it.
                        try:
                            fresh = self._read_active()
                        except Exception:
                            fresh = None
                        if fresh is not None and fresh.identity != candidate.identity:
                            self._offer_candidate(fresh, source="reconciliation")
                            continue
                        self._graceful_shutdown_after_ack(acknowledgement_generation)
                        raise ReleaseCandidateError(category)
                    continue

                if time.monotonic() >= next_reconcile:
                    next_reconcile = time.monotonic() + self._config.reconcile_interval
                    try:
                        reconciled = self._read_active()
                        self._offer_candidate(reconciled, source="reconciliation")
                    except Exception:
                        if not applied_once:
                            raise ReleaseStartupError(
                                "unable to reconcile the initial active configuration release"
                            ) from None
                        self._set_transport_failure("active_check_failed")
        finally:
            self.stop()
            self._relay_done.set()
            if self._relay_thread is not None:
                self._relay_thread.join(timeout=1.0)
            if self._watch_thread is not None:
                self._watch_thread.join(timeout=2.0)
            if self._executor is not None:
                self._executor.shutdown(wait=True, cancel_futures=True)
            with self._run_lock:
                if self._run_generation == run_generation:
                    self._running = False

    # --- candidate lifecycle ---------------------------------------------

    def _read_active(self) -> _Candidate:
        try:
            response = self._stub.GetActiveRelease(
                kms_pb2.GetActiveReleaseRequest(
                    namespace=to_proto_namespace(self._namespace), name=self._config.name
                ),
                metadata=self._client._auth_metadata(),
                timeout=self._client._call_timeout(self._config.request_timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        return _Candidate(_clone_release(response.release), response.activation_revision)

    def _offer_candidate(self, candidate: _Candidate, *, source: str = "activation") -> None:
        if not candidate.release.name:  # type: ignore[attr-defined]
            return
        accepted = False
        with self._candidate_cond:
            if candidate.revision and candidate.revision < self._last_seen_revision:
                return
            if candidate.revision > self._last_seen_revision:
                self._last_seen_revision = candidate.revision
            if self._latest_identity is not None:
                if candidate.revision < self._latest_identity[0]:
                    return
                if candidate.identity == self._latest_identity:
                    if not (
                        source == "reconciliation"
                        and self._retry_identity == candidate.identity
                    ):
                        return
            self._latest_identity = candidate.identity
            self._retry_identity = None
            if self._active_identity is not None and candidate.identity != self._active_identity:
                if self._active_cancel is not None:
                    self._active_cancel.set()
            self._pending_candidate = candidate
            self._candidate_cond.notify_all()
            accepted = True
        if accepted:
            with self._state_lock:
                self._candidates += 1
                self._status = replace(
                    self._status,
                    observed_version=candidate.release.version,  # type: ignore[attr-defined]
                    observed_revision=candidate.revision,
                )

    def _record_retry_eligibility(self, candidate: _Candidate, outcome: str) -> None:
        """Allow only the exact latest rejected identity to retry on reconciliation."""
        with self._candidate_cond:
            if outcome == "rejected" and self._latest_identity == candidate.identity:
                self._retry_identity = candidate.identity
            elif self._retry_identity == candidate.identity:
                self._retry_identity = None

    def _take_candidate(self, timeout: float) -> Optional[_Candidate]:
        deadline = time.monotonic() + timeout
        with self._candidate_cond:
            while self._pending_candidate is None and not self._stop_event.is_set():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    return None
                self._candidate_cond.wait(remaining)
            candidate = self._pending_candidate
            self._pending_candidate = None
            return candidate

    def _process_candidate(
        self,
        candidate: _Candidate,
        prepare_callback: Callable[[threading.Event, ReleaseSnapshot], PreparedRelease],
    ) -> Tuple[str, str, int]:
        cancel = threading.Event()
        with self._candidate_cond:
            self._active_cancel = cancel
            self._active_identity = candidate.identity
        self._set_observed(candidate)

        try:
            started = time.monotonic()
            try:
                snapshot = self._resolve(candidate, cancel)
            finally:
                self._record_resolution(int((time.monotonic() - started) * 1000))
            self._set_observed(candidate, "received")
            self._ack(candidate, "received")
            if cancel.is_set():
                raise _CandidateFailure("superseded")

            try:
                prepared = prepare_callback(cancel, snapshot)
            except Exception as exc:
                category = _classified_rejection_category(exc) or "prepare_failed"
                if cancel.is_set():
                    category = "superseded"
                raise _CandidateFailure(category) from None
            if prepared is None or not callable(getattr(prepared, "commit", None)) or not callable(
                getattr(prepared, "abort", None)
            ):
                raise _CandidateFailure("prepare_failed", "invalid prepared release")

            if cancel.is_set():
                self._abort_or_fail(candidate, prepared)
                raise _CandidateFailure("superseded")

            self._set_observed(candidate, "prepared")
            self._ack(candidate, "prepared")

            try:
                active = self._read_active()
            except Exception:
                self._abort_or_fail(candidate, prepared)
                raise _CandidateFailure("active_check_failed") from None
            if cancel.is_set():
                self._abort_or_fail(candidate, prepared)
                raise _CandidateFailure("superseded")
            if active.identity != candidate.identity:
                self._abort_or_fail(candidate, prepared)
                raise _CandidateFailure("superseded")

            try:
                returned: object = getattr(prepared, "commit")()
                if returned is not None:
                    raise TypeError("PreparedRelease.commit must return None")
            except BaseException:
                self._set_failure("internal")
                raise ReleaseCommitError(
                    "PreparedRelease.commit raised; applied state is unknown"
                ) from None

            with self._state_lock:
                self._status = replace(
                    self._status,
                    state="applied",
                    observed_version=candidate.release.version,  # type: ignore[attr-defined]
                    observed_revision=candidate.revision,
                    applied_version=candidate.release.version,  # type: ignore[attr-defined]
                    applied_revision=candidate.revision,
                    last_failure_category="",
                    last_failure_unix_ms=0,
                )
                self._applied += 1
            self._ack(candidate, "applied", divergence=_divergence_of(prepared))
            return "applied", "", 0
        except _CandidateFailure as exc:
            acknowledgement_generation = self._reject(
                candidate, exc.category, exc.diagnostic
            )
            if exc.category == "superseded":
                return "superseded", exc.category, acknowledgement_generation
            return "rejected", exc.category, acknowledgement_generation
        finally:
            with self._candidate_cond:
                if self._active_cancel is cancel:
                    self._active_cancel = None
                    self._active_identity = None

    def _abort_or_fail(self, candidate: _Candidate, prepared: PreparedRelease) -> None:
        """Abort synchronously; contract violations are fatal internal failures."""
        try:
            returned: object = getattr(prepared, "abort")()
            if returned is not None:
                raise TypeError("PreparedRelease.abort must return None")
        except BaseException:
            self._reject(candidate, "internal")
            raise ReleaseCommitError(
                "PreparedRelease.abort failed; abort must be infallible and return None"
            ) from None

    def _resolve(self, candidate: _Candidate, cancel: threading.Event) -> ReleaseSnapshot:
        release = candidate.release
        if (
            not release.name
            or release.name != self._config.name
            or release.namespace.env != self._namespace.env
            or release.namespace.app != self._namespace.app
        ):
            raise _CandidateFailure("version_mismatch", "release identity mismatch")
        try:
            calculated_digest = _release_digest(release)
        except (TypeError, ValueError):
            raise _CandidateFailure("digest_mismatch", "release digest mismatch") from None
        if not _valid_sha256_hex(release.digest) or not hmac.compare_digest(
            calculated_digest, release.digest.lower()
        ):
            raise _CandidateFailure("digest_mismatch", "release digest mismatch")
        if not release.entries:
            raise _CandidateFailure("resolution_failed", "release has no entries")
        try:
            entries = tuple(_entry_from_proto(entry) for entry in release.entries)
        except (TypeError, ValueError):
            raise _CandidateFailure("resolution_failed", "invalid release entry") from None
        if len({entry.alias for entry in entries}) != len(entries):
            raise _CandidateFailure("resolution_failed", "duplicate alias")
        if any(
            split_display_path(entry.path).ns != self._namespace
            for entry in entries
        ):
            raise _CandidateFailure("resolution_failed", "release entry namespace mismatch")

        manifest = ReleaseManifest(
            namespace=f"{release.namespace.env}/{release.namespace.app}",
            name=release.name,
            version=release.version,
            activation_revision=candidate.revision,
            schema_version=release.schema_version,
            digest=release.digest,
            metadata_json=release.metadata_json,
            entries=MappingProxyType({entry.alias: entry for entry in entries}),
        )
        validator = self._config.validate_manifest
        if validator is not None:
            try:
                validator(cancel, manifest)
            except Exception as exc:
                category = _classified_rejection_category(exc) or "prepare_failed"
                if cancel.is_set():
                    category = "superseded"
                raise _CandidateFailure(category) from None
        if cancel.is_set() or self._stop_event.is_set():
            raise _CandidateFailure("superseded")

        executor = self._executor
        if executor is None:
            raise _CandidateFailure("internal")
        futures = {
            executor.submit(self._resolve_entry, entry, cancel): entry for entry in entries
        }
        parameters: Dict[str, str] = {}
        secrets: Dict[str, Secret] = {}
        pending = set(futures)
        try:
            while pending:
                if cancel.is_set() or self._stop_event.is_set():
                    for future in pending:
                        future.cancel()
                    raise _CandidateFailure("superseded")
                done, pending = concurrent.futures.wait(
                    pending, timeout=0.05, return_when=concurrent.futures.FIRST_COMPLETED
                )
                for future in done:
                    entry = futures[future]
                    try:
                        value = future.result()
                    except _CandidateFailure:
                        raise
                    except Exception:
                        raise _CandidateFailure("resolution_failed") from None
                    if entry.kind == _KIND_PARAMETER:
                        parameters[entry.alias] = value
                    else:
                        secrets[entry.alias] = value
        finally:
            for future in pending:
                future.cancel()

        return ReleaseSnapshot(
            namespace=manifest.namespace,
            name=manifest.name,
            version=manifest.version,
            activation_revision=manifest.activation_revision,
            schema_version=manifest.schema_version,
            digest=manifest.digest,
            metadata_json=manifest.metadata_json,
            entries=entries,
            parameters=MappingProxyType(parameters),
            secrets=MappingProxyType(secrets),
        )

    def _resolve_entry(self, entry: ReleaseEntry, cancel: threading.Event):
        if cancel.is_set() or self._stop_event.is_set():
            raise _CandidateFailure("superseded")
        proto_ref = to_proto_ref(split_display_path(entry.path))
        timeout = self._client._call_timeout(self._config.request_timeout)
        if entry.kind == _KIND_PARAMETER:
            try:
                response = self._client._param_stub.GetParameter(
                    kms_pb2.GetParameterRequest(ref=proto_ref, version=entry.version),
                    metadata=self._client._auth_metadata(),
                    timeout=timeout,
                )
            except grpc.RpcError as exc:
                raise _CandidateFailure("resolution_failed", _grpc_code_name(exc)) from None
            parameter = response.parameter
            if parameter.version != entry.version:
                raise _CandidateFailure("version_mismatch")
            if str(Ref(NamespaceRef(parameter.ref.namespace.env, parameter.ref.namespace.app), parameter.ref.key)) != entry.path:
                raise _CandidateFailure("version_mismatch", "resource mismatch")
            if entry.content_type and parameter.content_type != entry.content_type:
                raise _CandidateFailure("digest_mismatch", "content type mismatch")
            digest = hashlib.sha256(parameter.value.encode("utf-8")).hexdigest()
            if not _valid_sha256_hex(entry.parameter_digest) or not hmac.compare_digest(
                digest, entry.parameter_digest.lower()
            ):
                raise _CandidateFailure("digest_mismatch")
            return parameter.value

        if entry.kind != _KIND_SECRET:
            raise _CandidateFailure("resolution_failed", "unknown entry kind")
        try:
            live = self._client.get_secret_metadata(
                entry.path, timeout=self._config.request_timeout,
            )
        except Exception:
            raise _CandidateFailure("resolution_failed") from None
        requested_ref = split_display_path(entry.path)
        if (live.env, live.app, live.key) != (
            requested_ref.ns.env, requested_ref.ns.app, requested_ref.key,
        ):
            raise _CandidateFailure("version_mismatch", "resource mismatch")
        matches = tuple(item for item in live.versions if item.version == entry.version)
        if len(matches) != 1 or matches[0].state != "enabled":
            raise _CandidateFailure("resolution_failed")
        exact = matches[0]
        if exact.expires_at_unix_ms > 0 and exact.expires_at_unix_ms <= _now_ms():
            raise _CandidateFailure("resolution_failed")

        secret_token = ""
        if exact.has_access_token:
            provider = self._config.secret_token_provider
            if provider is None:
                raise _CandidateFailure("token_unavailable")
            try:
                secret_token = _token_from_result(provider(entry.alias, entry.path))
            except Exception:
                raise _CandidateFailure("token_unavailable") from None
            if not secret_token:
                raise _CandidateFailure("token_unavailable")
        binding_key = ""
        if exact.bound:
            binding_key = self._binding_keys.get(entry.alias, "")
            if not isinstance(binding_key, str) or not binding_key:
                raise _CandidateFailure("token_unavailable")
        try:
            secret = self._client.get_secret(
                entry.path,
                version=entry.version,
                secret_token=secret_token,
                binding_key=binding_key,
                timeout=self._config.request_timeout,
            )
        except Exception:
            raise _CandidateFailure("resolution_failed") from None
        if secret.version != entry.version:
            raise _CandidateFailure("version_mismatch")
        if secret.path != entry.path:
            raise _CandidateFailure("version_mismatch", "resource mismatch")
        if entry.content_type and secret.content_type != entry.content_type:
            raise _CandidateFailure("version_mismatch", "content type mismatch")
        return secret

    # --- watch and acknowledgement transport -----------------------------

    def _watch_loop(self) -> None:
        attempt = 0
        streams_started = 0
        try:
            while not self._stop_event.is_set() and not self._graceful_watch_stop.is_set():
                if streams_started:
                    with self._state_lock:
                        self._reconnects += 1
                        self._status = replace(
                            self._status, reconnects=self._reconnects
                        )
                streams_started += 1
                if self._watch_once():
                    # A usable connection may still terminate with an RPC error.
                    # Receiving any server event proves connectivity and resets the
                    # next delay to the base window.
                    attempt = 0
                if self._stop_event.is_set() or self._graceful_watch_stop.is_set():
                    return
                cap = min(
                    self._config.reconnect_max,
                    self._config.reconnect_initial * (2 ** min(attempt, 16)),
                )
                attempt += 1
                self._stop_event.wait(random.uniform(0.01, cap))
        finally:
            self._watch_done.set()

    def _watch_once(self) -> bool:
        received_event = False
        requests = self._watch_requests()
        call = None
        try:
            call = self._stub.WatchRelease(requests, metadata=self._client._auth_metadata())
            with self._call_lock:
                self._watch_call = call
            for event in call:
                if self._stop_event.is_set():
                    return received_event
                received_event = True
                kind = event.WhichOneof("event")
                if kind == "snapshot":
                    self._offer_candidate(
                        _Candidate(_clone_release(event.snapshot.release), event.revision)
                    )
                elif kind == "activation":
                    self._offer_candidate(
                        _Candidate(_clone_release(event.activation.release), event.revision)
                    )
        except Exception:
            return received_event
        finally:
            with self._call_lock:
                if self._watch_call is call:
                    self._watch_call = None
        return received_event

    def _watch_requests(self):
        with self._candidate_cond:
            last_seen = self._last_seen_revision
        yield kms_pb2.WatchReleaseRequest(
            register=kms_pb2.ReleaseWatchRegistration(
                namespace=to_proto_namespace(self._namespace),
                name=self._config.name,
                client_name=self._client_name,
                instance_id=self._instance_id,
                last_seen_revision=last_seen,
            )
        )

        with self._ack_cond:
            initial = sorted(self._ack_latest.values(), key=lambda item: item[0])
            sent_sequence = max((item[0] for item in initial), default=0)
        for sequence, acknowledgement in initial:
            yield kms_pb2.WatchReleaseRequest(acknowledgement=acknowledgement)
            with self._ack_cond:
                self._ack_flushed_by_state[acknowledgement.state] = sequence
                self._ack_cond.notify_all()

        while not self._stop_event.is_set() and not self._graceful_watch_stop.is_set():
            with self._ack_cond:
                updates = sorted(
                    (item for item in self._ack_latest.values() if item[0] > sent_sequence),
                    key=lambda item: item[0],
                )
                if not updates:
                    self._ack_cond.wait(timeout=0.5)
                    continue
            for sequence, acknowledgement in updates:
                sent_sequence = max(sent_sequence, sequence)
                yield kms_pb2.WatchReleaseRequest(acknowledgement=acknowledgement)
                with self._ack_cond:
                    self._ack_flushed_by_state[acknowledgement.state] = sequence
                    self._ack_cond.notify_all()

    def _graceful_shutdown_after_ack(self, generation: int) -> None:
        """Flush the exact terminal ack, half-close, and drain the stream."""
        timeout = min(2.0, self._client._call_timeout(self._config.request_timeout))
        deadline = time.monotonic() + timeout
        with self._ack_cond:
            while (
                self._ack_flushed_by_state.get("rejected") != generation
                and not self._stop_event.is_set()
            ):
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    break
                self._ack_cond.wait(remaining)
            self._graceful_watch_stop.set()
            self._ack_cond.notify_all()
        remaining = max(0.0, deadline - time.monotonic())
        if self._watch_done.wait(remaining):
            return
        with self._call_lock:
            call = self._watch_call
        if call is not None:
            try:
                call.cancel()
            except Exception:
                pass

    def _ack(
        self,
        candidate: _Candidate,
        state: str,
        category: str = "",
        diagnostic: str = "",
        divergence: Tuple[bool, int] = (False, 0),
    ) -> int:
        if state not in _STATES:
            return 0
        if state != "rejected":
            category = ""
            diagnostic = ""
        elif category not in _REJECTION_CATEGORIES:
            category = "internal"
        acknowledgement = kms_pb2.ReleaseAcknowledgement(
            namespace=to_proto_namespace(self._namespace),
            name=self._config.name,
            version=candidate.release.version,  # type: ignore[attr-defined]
            activation_revision=candidate.revision,
            client_name=self._client_name,
            instance_id=self._instance_id,
            state=state,
            rejection_category=category,
            # Local errors can contain resolved values. Categories are the
            # complete wire contract; arbitrary diagnostics never cross it.
            diagnostic="",
            timestamp_unix_ms=_now_ms(),
            applied_divergent=state == "applied" and divergence[0],
            divergent_field_count=divergence[1] if state == "applied" and divergence[0] else 0,
        )
        with self._ack_cond:
            current = self._ack_latest.get(state)
            if current is None or current[1].activation_revision <= candidate.revision:
                self._ack_sequence += 1
                generation = self._ack_sequence
                self._ack_latest[state] = (generation, acknowledgement)
                self._ack_cond.notify_all()
                accepted = True
            else:
                generation = 0
                accepted = False
        if accepted:
            with self._state_lock:
                self._ack_counts[state] += 1
        return generation

    def _reject(self, candidate: _Candidate, category: str, diagnostic: str = "") -> int:
        self._set_failure(category)
        with self._state_lock:
            self._resolution_failures += int(
                category
                in {
                    "resolution_failed",
                    "token_unavailable",
                    "version_mismatch",
                    "digest_mismatch",
                }
            )
        return self._ack(candidate, "rejected", category, diagnostic)

    # --- status helpers ---------------------------------------------------

    def _set_observed(self, candidate: _Candidate, state: Optional[str] = None) -> None:
        with self._state_lock:
            if state is None:
                self._status = replace(
                    self._status,
                    observed_version=candidate.release.version,  # type: ignore[attr-defined]
                    observed_revision=candidate.revision,
                )
            else:
                self._status = replace(
                    self._status,
                    state=state,
                    observed_version=candidate.release.version,  # type: ignore[attr-defined]
                    observed_revision=candidate.revision,
                )

    def _record_resolution(self, elapsed_ms: int) -> None:
        with self._state_lock:
            self._resolutions += 1
            self._last_resolution_ms = max(0, elapsed_ms)
            self._status = replace(
                self._status,
                last_resolution_duration_ms=self._last_resolution_ms,
            )

    def _set_failure(self, category: str) -> None:
        if category not in _REJECTION_CATEGORIES:
            category = "internal"
        with self._state_lock:
            self._rejection_counts[category] += 1
            self._status = replace(
                self._status,
                state="rejected",
                last_failure_category=category,
                last_failure_unix_ms=_now_ms(),
            )

    def _set_transport_failure(self, category: str) -> None:
        """Record an outage without presenting the last-known-good as rejected."""
        if category not in _REJECTION_CATEGORIES:
            category = "internal"
        with self._state_lock:
            self._status = replace(
                self._status,
                last_failure_category=category,
                last_failure_unix_ms=_now_ms(),
            )


T = TypeVar("T")


def run_typed_release(
    loader: ReleaseLoader,
    decode: Callable[[ReleaseSnapshot], T],
    prepare: Callable[[threading.Event, T], PreparedRelease],
    *,
    stop_event: Optional[threading.Event] = None,
) -> None:
    """Run a loader with explicit typed decoding and no reflection."""

    def prepare_snapshot(cancel: threading.Event, snapshot: ReleaseSnapshot) -> PreparedRelease:
        return prepare(cancel, decode(snapshot))

    loader.run(prepare_snapshot, stop_event=stop_event)


def _entry_from_proto(entry) -> ReleaseEntry:
    ref = entry.ref
    if (
        not entry.alias
        or entry.kind not in {_KIND_PARAMETER, _KIND_SECRET}
        or entry.version <= 0
        or not ref.namespace.env
        or not ref.namespace.app
        or not ref.key
    ):
        raise ValueError("invalid release entry")
    return ReleaseEntry(
        alias=entry.alias,
        kind=entry.kind,
        path=str(Ref(NamespaceRef(ref.namespace.env, ref.namespace.app), ref.key)),
        version=entry.version,
        content_type=entry.content_type,
        metadata_json=entry.metadata_json,
        parameter_digest=entry.parameter_digest,
    )


def _release_digest(release) -> str:
    """Hash the same immutable, alias-sorted projection as the server."""
    projection = kms_pb2.ConfigurationRelease(
        namespace=kms_pb2.NamespaceRef(
            env=release.namespace.env,
            app=release.namespace.app,
        ),
        name=release.name,
        schema_version=release.schema_version,
        metadata_json=release.metadata_json,
    )
    for source in sorted(release.entries, key=lambda entry: entry.alias):
        entry = projection.entries.add(
            alias=source.alias,
            kind=source.kind,
            version=source.version,
            content_type=source.content_type,
            metadata_json=source.metadata_json,
            parameter_digest=source.parameter_digest,
        )
        entry.ref.CopyFrom(source.ref)
    encoded = projection.SerializeToString(deterministic=True)
    return hashlib.sha256(encoded).hexdigest()


def _clone_release(release):
    clone = kms_pb2.ConfigurationRelease()
    clone.CopyFrom(release)
    return clone


def _is_newer(
    candidate: Tuple[int, int, str, str], previous: Tuple[int, int, str, str]
) -> bool:
    if candidate[0] != previous[0]:
        return candidate[0] > previous[0]
    return candidate[1:] != previous[1:]


def _valid_sha256_hex(value: object) -> bool:
    if not isinstance(value, str) or len(value) != 64:
        return False
    try:
        decoded = bytes.fromhex(value)
    except (TypeError, ValueError):
        return False
    return value.isascii() and len(decoded) == 32


def _token_from_result(result: SecretTokenResult) -> str:
    if isinstance(result, tuple):
        if len(result) != 2:
            return ""
        token, ok = result
        return token if isinstance(token, str) and ok is True else ""
    return result if isinstance(result, str) else ""


def _classified_rejection_category(exc: BaseException) -> str:
    """Read only an allow-listed category; never preserve the local cause."""
    try:
        category = getattr(exc, "release_rejection_category", "")
        if not category:
            method = getattr(exc, "release_rejection_category_value", None)
            if callable(method):
                category = method()
    except BaseException:
        return ""
    return category if isinstance(category, str) and category in _REJECTION_CATEGORIES else ""


def _divergence_of(prepared: PreparedRelease) -> Tuple[bool, int]:
    reporter = getattr(prepared, "release_divergence", None)
    if not callable(reporter):
        return (False, 0)
    try:
        reported = reporter()
        if not isinstance(reported, tuple) or len(reported) != 2:
            return (False, 0)
        divergent, field_count = reported
        if divergent is not True or isinstance(field_count, bool) or not isinstance(field_count, int):
            return (False, 0)
        return (True, min(max(field_count, 0), _MAX_DIVERGENT_FIELD_COUNT))
    except BaseException:
        return (False, 0)


def _grpc_code_name(exc: grpc.RpcError) -> str:
    if isinstance(exc, grpc.Call):
        code = exc.code()
        if code is not None:
            return code.name.lower()[:_DIAGNOSTIC_LIMIT]
    return "rpc_error"


def _now_ms() -> int:
    return int(time.time() * 1000)
