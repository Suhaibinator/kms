"""Asyncio-native configuration release loading.

The async loader intentionally owns its own ``grpc.aio`` stream, tasks, and
cancellation events.  No synchronous loader state, thread, or channel crosses
the event-loop boundary.
"""

from __future__ import annotations

import asyncio
import hashlib
import inspect
import random
import time
import uuid
from dataclasses import dataclass, replace
from types import MappingProxyType
from typing import Any, Awaitable, Callable, Dict, Mapping, Optional, Tuple, Union

import grpc

from . import errors
from ._gen import kms_pb2, kms_pb2_grpc
from ._refs import NamespaceRef, Ref, split_display_path, to_proto_namespace, to_proto_ref
from .release import (
    PreparedRelease,
    ReleaseCandidateError,
    ReleaseCommitError,
    ReleaseEntry,
    ReleaseManifest,
    ReleaseSnapshot,
    ReleaseStartupError,
    ReleaseStats,
    ReleaseStatus,
    _Candidate,
    _CandidateFailure,
    _REJECTION_CATEGORIES,
    _STATES,
    _classified_rejection_category,
    _clone_release,
    _divergence_of,
    _entry_from_proto,
    _grpc_code_name,
    _now_ms,
    _release_digest,
    _token_from_result,
    _validate_release_timing,
    _valid_sha256_hex,
)
from .secret import Secret

AsyncTokenResult = Union[str, Tuple[str, bool], None]
AsyncSecretTokenProvider = Callable[
    [str, str, asyncio.Event], Union[AsyncTokenResult, Awaitable[AsyncTokenResult]]
]
AsyncManifestValidator = Callable[
    [asyncio.Event, ReleaseManifest], Union[None, Awaitable[None]]
]


@dataclass(frozen=True)
class AsyncReleaseLoaderConfig:
    """Event-loop-owned release loader settings."""

    name: str
    namespace: "Optional[str | NamespaceRef]" = None
    reconcile_interval: float = 60.0
    secret_token_provider: Optional[AsyncSecretTokenProvider] = None
    validate_manifest: Optional[AsyncManifestValidator] = None
    max_concurrent_fetches: int = 16
    client_name: Optional[str] = None
    instance_id: Optional[str] = None
    reconnect_initial: float = 0.25
    reconnect_max: float = 30.0
    request_timeout: Optional[float] = None


class AsyncReleaseLoader:
    """Resolve and atomically apply releases using only asyncio primitives."""

    def __init__(self, client: Any, config: AsyncReleaseLoaderConfig) -> None:
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
        self._namespace: Optional[NamespaceRef] = None
        self._client_name = config.client_name or client._client_name
        self._instance_id = config.instance_id or str(uuid.uuid4())
        self._stub = kms_pb2_grpc.ConfigurationReleaseServiceStub(client._channel)
        self._running = False
        self._stop_event = asyncio.Event()
        self._candidate_queue: "asyncio.Queue[_Candidate]" = asyncio.Queue(maxsize=1)
        self._active_cancel: Optional[asyncio.Event] = None
        self._active_identity: Optional[Tuple[int, int, str, str]] = None
        self._latest_identity: Optional[Tuple[int, int, str, str]] = None
        self._retry_identity: Optional[Tuple[int, int, str, str]] = None
        self._last_seen_revision = 0
        self._watch_call: Any = None

        self._ack_event = asyncio.Event()
        self._ack_generation = 0
        self._ack_flushed_by_state: Dict[str, int] = {}
        self._ack_latest: Dict[str, Tuple[int, Any, bool]] = {}
        self._graceful_watch_stop = asyncio.Event()
        self._watch_done = asyncio.Event()

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
        return self._status

    def stats(self) -> ReleaseStats:
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
        self._stop_event.set()
        if self._active_cancel is not None:
            self._active_cancel.set()
        self._ack_event.set()
        call = self._watch_call
        if call is not None:
            try:
                call.cancel()
            except Exception:
                pass

    async def run(
        self,
        prepare: Callable[
            [asyncio.Event, ReleaseSnapshot],
            Union[PreparedRelease, Awaitable[PreparedRelease]],
        ],
        *,
        stop_event: Optional[asyncio.Event] = None,
    ) -> None:
        if not callable(prepare):
            raise errors.ConfigError("release prepare callback is required")
        if self._running:
            raise errors.ConfigError("an AsyncReleaseLoader is already running")
        self._running = True
        self._stop_event = asyncio.Event()
        self._candidate_queue = asyncio.Queue(maxsize=1)
        self._active_cancel = None
        self._active_identity = None
        self._latest_identity = None
        self._retry_identity = None
        self._graceful_watch_stop = asyncio.Event()
        self._watch_done = asyncio.Event()
        try:
            namespace = self._client._resolve_namespace_arg(self._config.namespace)
            if inspect.isawaitable(namespace):
                namespace = await namespace
            self._namespace = namespace

            try:
                initial = await self._read_active()
            except Exception:
                raise ReleaseStartupError(
                    "unable to read the initial active configuration release"
                ) from None
            if not initial.release.name:
                raise ReleaseStartupError(
                    "active configuration release response was empty"
                )
            self._last_seen_revision = initial.revision
            watch_task = asyncio.create_task(self._watch_loop(), name="kms-release-watch")
            reconcile_task = asyncio.create_task(
                self._reconcile_loop(), name="kms-release-reconcile"
            )
            relay_task = (
                asyncio.create_task(self._relay_stop(stop_event), name="kms-release-stop")
                if stop_event is not None
                else None
            )
            self._offer_candidate(initial, source="reconciliation")
            applied_once = False
            try:
                while not self._stop_event.is_set():
                    candidate_task = asyncio.create_task(self._candidate_queue.get())
                    stopped_task = asyncio.create_task(self._stop_event.wait())
                    done, _ = await asyncio.wait(
                        {candidate_task, stopped_task},
                        return_when=asyncio.FIRST_COMPLETED,
                    )
                    if stopped_task in done:
                        candidate_task.cancel()
                        await asyncio.gather(candidate_task, return_exceptions=True)
                        break
                    stopped_task.cancel()
                    await asyncio.gather(stopped_task, return_exceptions=True)
                    candidate = candidate_task.result()
                    outcome, category, acknowledgement_generation = (
                        await self._process_candidate(candidate, prepare)
                    )
                    self._record_retry_eligibility(candidate, outcome)
                    if outcome == "applied":
                        applied_once = True
                    elif outcome == "rejected" and not applied_once:
                        try:
                            fresh = await self._read_active()
                        except Exception:
                            fresh = None
                        if fresh is not None and fresh.identity != candidate.identity:
                            self._offer_candidate(fresh, source="reconciliation")
                            continue
                        await self._graceful_shutdown_after_ack(
                            acknowledgement_generation
                        )
                        raise ReleaseCandidateError(category)
            finally:
                self.stop()
                tasks = [watch_task, reconcile_task]
                if relay_task is not None:
                    tasks.append(relay_task)
                for task in tasks:
                    task.cancel()
                await asyncio.gather(*tasks, return_exceptions=True)
        finally:
            self._watch_call = None
            self._running = False

    async def _relay_stop(self, stop_event: asyncio.Event) -> None:
        await stop_event.wait()
        self.stop()

    def _offer_candidate(self, candidate: _Candidate, *, source: str = "activation") -> None:
        if self._stop_event.is_set() or not candidate.release.name:
            return
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
        if self._candidate_queue.full():
            try:
                self._candidate_queue.get_nowait()
            except asyncio.QueueEmpty:
                pass
        self._candidate_queue.put_nowait(candidate)
        self._candidates += 1
        self._status = replace(
            self._status,
            observed_version=candidate.release.version,
            observed_revision=candidate.revision,
        )

    def _record_retry_eligibility(self, candidate: _Candidate, outcome: str) -> None:
        """Allow only the exact latest rejected identity to retry on reconciliation."""
        if outcome == "rejected" and self._latest_identity == candidate.identity:
            self._retry_identity = candidate.identity
        elif self._retry_identity == candidate.identity:
            self._retry_identity = None

    async def _process_candidate(
        self, candidate: _Candidate, prepare: Any
    ) -> Tuple[str, str, int]:
        cancel = asyncio.Event()
        self._active_cancel = cancel
        self._active_identity = candidate.identity
        self._status = replace(
            self._status,
            observed_version=candidate.release.version,
            observed_revision=candidate.revision,
        )
        prepared: Optional[PreparedRelease] = None
        try:
            started = time.monotonic()
            try:
                snapshot = await self._resolve(candidate, cancel)
            finally:
                self._record_resolution(int((time.monotonic() - started) * 1000))
            self._status = replace(self._status, state="received")
            self._ack(candidate, "received")
            if cancel.is_set():
                raise _CandidateFailure("superseded")
            try:
                result = prepare(cancel, snapshot)
                prepared = await result if inspect.isawaitable(result) else result
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                category = _classified_rejection_category(exc) or "prepare_failed"
                if cancel.is_set():
                    category = "superseded"
                raise _CandidateFailure(category) from None
            if prepared is None or not callable(getattr(prepared, "commit", None)) or not callable(
                getattr(prepared, "abort", None)
            ):
                raise _CandidateFailure("prepare_failed")
            if cancel.is_set():
                self._abort_or_fail(candidate, prepared)
                raise _CandidateFailure("superseded")
            self._status = replace(self._status, state="prepared")
            self._ack(candidate, "prepared")
            try:
                active = await self._read_active()
            except Exception:
                self._abort_or_fail(candidate, prepared)
                raise _CandidateFailure(
                    "superseded" if cancel.is_set() else "active_check_failed"
                ) from None
            if cancel.is_set() or active.identity != candidate.identity:
                self._abort_or_fail(candidate, prepared)
                raise _CandidateFailure("superseded")
            try:
                returned = prepared.commit()
                if returned is not None:
                    raise TypeError("PreparedRelease.commit must return None")
            except BaseException:
                self._rejection_counts["internal"] += 1
                self._status = replace(
                    self._status,
                    state="rejected",
                    last_failure_category="internal",
                    last_failure_unix_ms=_now_ms(),
                )
                raise ReleaseCommitError(
                    "PreparedRelease.commit raised; applied state is unknown"
                ) from None
            self._status = replace(
                self._status,
                state="applied",
                observed_version=candidate.release.version,
                observed_revision=candidate.revision,
                applied_version=candidate.release.version,
                applied_revision=candidate.revision,
                last_failure_category="",
                last_failure_unix_ms=0,
            )
            self._applied += 1
            self._ack(candidate, "applied", divergence=_divergence_of(prepared))
            return ("applied", "", 0)
        except _CandidateFailure as exc:
            if prepared is not None and exc.category != "prepare_failed":
                # All paths that need cleanup normally abort above. This guard
                # covers validator/fencing refactors without double-aborting.
                pass
            generation = self._reject(candidate, exc.category)
            return (
                "superseded" if exc.category == "superseded" else "rejected",
                exc.category,
                generation,
            )
        except asyncio.CancelledError:
            if prepared is not None:
                self._abort_or_fail(candidate, prepared)
            raise
        finally:
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

    async def _resolve(self, candidate: _Candidate, cancel: asyncio.Event) -> ReleaseSnapshot:
        release = candidate.release
        namespace = self._require_namespace()
        if (
            not release.name
            or release.name != self._config.name
            or release.namespace.env != namespace.env
            or release.namespace.app != namespace.app
        ):
            raise _CandidateFailure("version_mismatch")
        try:
            calculated_digest = _release_digest(release)
        except (TypeError, ValueError):
            raise _CandidateFailure("digest_mismatch") from None
        if (
            not _valid_sha256_hex(release.digest)
            or release.digest.lower() != calculated_digest
        ):
            raise _CandidateFailure("digest_mismatch")
        if not release.entries:
            raise _CandidateFailure("resolution_failed")
        try:
            entries = tuple(_entry_from_proto(entry) for entry in release.entries)
        except (TypeError, ValueError):
            raise _CandidateFailure("resolution_failed") from None
        if len({entry.alias for entry in entries}) != len(entries):
            raise _CandidateFailure("resolution_failed")
        manifest = ReleaseManifest(
            namespace=f"{namespace.env}/{namespace.app}",
            name=release.name,
            version=release.version,
            activation_revision=candidate.revision,
            schema_id=release.schema_id,
            schema_version=release.schema_version,
            digest=release.digest,
            metadata_json=release.metadata_json,
            entries=MappingProxyType({entry.alias: entry for entry in entries}),
        )
        validator = self._config.validate_manifest
        if validator is not None:
            try:
                result = validator(cancel, manifest)
                if inspect.isawaitable(result):
                    await result
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                raise _CandidateFailure(
                    "superseded"
                    if cancel.is_set()
                    else (_classified_rejection_category(exc) or "prepare_failed")
                ) from None
        if cancel.is_set() or self._stop_event.is_set():
            raise _CandidateFailure("superseded")
        semaphore = asyncio.Semaphore(self._config.max_concurrent_fetches)

        async def resolve_one(entry: ReleaseEntry) -> Tuple[ReleaseEntry, Any]:
            async with semaphore:
                return (entry, await self._resolve_entry(entry, cancel))

        tasks = [asyncio.create_task(resolve_one(entry)) for entry in entries]
        parameters: Dict[str, str] = {}
        secrets: Dict[str, Secret] = {}
        try:
            results = await _gather_until_cancel(tasks, cancel)
            for entry, value in results:
                if entry.kind == "parameter":
                    parameters[entry.alias] = value
                else:
                    secrets[entry.alias] = value
        finally:
            for task in tasks:
                if not task.done():
                    task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
        return ReleaseSnapshot(
            namespace=manifest.namespace,
            name=manifest.name,
            version=manifest.version,
            activation_revision=manifest.activation_revision,
            schema_id=manifest.schema_id,
            schema_version=manifest.schema_version,
            digest=manifest.digest,
            metadata_json=manifest.metadata_json,
            entries=entries,
            parameters=MappingProxyType(parameters),
            secrets=MappingProxyType(secrets),
        )

    async def _resolve_entry(self, entry: ReleaseEntry, cancel: asyncio.Event) -> Any:
        if cancel.is_set() or self._stop_event.is_set():
            raise _CandidateFailure("superseded")
        proto_ref = to_proto_ref(split_display_path(entry.path))
        timeout = self._client._call_timeout(self._config.request_timeout)
        if entry.kind == "parameter":
            try:
                response = await self._client._param_stub.GetParameter(
                    kms_pb2.GetParameterRequest(ref=proto_ref, version=entry.version),
                    metadata=self._client._auth_metadata(),
                    timeout=timeout,
                )
            except grpc.RpcError as exc:
                raise _CandidateFailure("resolution_failed", _grpc_code_name(exc)) from None
            parameter = response.parameter
            actual_path = str(
                Ref(
                    NamespaceRef(parameter.ref.namespace.env, parameter.ref.namespace.app),
                    parameter.ref.key,
                )
            )
            if parameter.version != entry.version or actual_path != entry.path:
                raise _CandidateFailure("version_mismatch")
            digest = hashlib.sha256(parameter.value.encode("utf-8")).hexdigest()
            if (
                not _valid_sha256_hex(entry.parameter_digest)
                or digest.lower() != entry.parameter_digest.lower()
            ):
                raise _CandidateFailure("digest_mismatch")
            if entry.content_type and parameter.content_type != entry.content_type:
                raise _CandidateFailure("digest_mismatch")
            return parameter.value
        token = ""
        if entry.client_bound or entry.has_access_token:
            provider = self._config.secret_token_provider
            if provider is None:
                raise _CandidateFailure("token_unavailable")
            try:
                result = provider(entry.alias, entry.path, cancel)
                token_result = await result if inspect.isawaitable(result) else result
                token = _token_from_result(token_result)
            except Exception:
                raise _CandidateFailure(
                    "superseded" if cancel.is_set() else "token_unavailable"
                ) from None
            if not token:
                raise _CandidateFailure("token_unavailable")
        try:
            secret = await self._client.get_secret(
                entry.path,
                version=entry.version,
                secret_token=token,
                timeout=self._config.request_timeout,
            )
        except Exception:
            raise _CandidateFailure(
                "superseded" if cancel.is_set() else "resolution_failed"
            ) from None
        if secret.version != entry.version or secret.path != entry.path:
            raise _CandidateFailure("version_mismatch")
        if entry.content_type and secret.content_type != entry.content_type:
            raise _CandidateFailure("version_mismatch")
        return secret

    async def _read_active(self) -> _Candidate:
        try:
            response = await self._stub.GetActiveRelease(
                kms_pb2.GetActiveReleaseRequest(
                    namespace=to_proto_namespace(self._require_namespace()),
                    name=self._config.name,
                ),
                metadata=self._client._auth_metadata(),
                timeout=self._client._call_timeout(self._config.request_timeout),
            )
        except grpc.RpcError as exc:
            raise errors.map_grpc_error(exc) from None
        return _Candidate(_clone_release(response.release), response.activation_revision)

    async def _reconcile_loop(self) -> None:
        while not self._stop_event.is_set():
            try:
                await asyncio.wait_for(
                    self._stop_event.wait(), timeout=self._config.reconcile_interval
                )
                return
            except asyncio.TimeoutError:
                pass
            try:
                candidate = await self._read_active()
            except Exception:
                if self._status.applied_version:
                    self._status = replace(
                        self._status,
                        last_failure_category="active_check_failed",
                        last_failure_unix_ms=_now_ms(),
                    )
                continue
            self._offer_candidate(candidate, source="reconciliation")

    async def _watch_loop(self) -> None:
        attempt = 0
        streams_started = 0
        try:
            while not self._stop_event.is_set() and not self._graceful_watch_stop.is_set():
                if streams_started:
                    self._reconnects += 1
                    self._status = replace(
                        self._status, reconnects=self._reconnects
                    )
                streams_started += 1
                received = await self._watch_once()
                if received:
                    attempt = 0
                if self._stop_event.is_set() or self._graceful_watch_stop.is_set():
                    return
                cap = min(
                    self._config.reconnect_max,
                    self._config.reconnect_initial * (2 ** min(attempt, 16)),
                )
                attempt += 1
                try:
                    await asyncio.wait_for(
                        self._stop_event.wait(), timeout=random.uniform(0.01, cap)
                    )
                except asyncio.TimeoutError:
                    pass
        finally:
            self._watch_done.set()

    async def _watch_once(self) -> bool:
        call: Any = None
        sender: Optional[asyncio.Task[None]] = None
        received = False
        try:
            call = self._stub.WatchRelease(metadata=self._client._auth_metadata())
            await call.write(
                kms_pb2.WatchReleaseRequest(
                    register=kms_pb2.ReleaseWatchRegistration(
                        namespace=to_proto_namespace(self._require_namespace()),
                        name=self._config.name,
                        client_name=self._client_name,
                        instance_id=self._instance_id,
                        last_seen_revision=self._last_seen_revision,
                    )
                )
            )
            await self._flush_acks(call, replay=True)
            self._watch_call = call
            sender = asyncio.create_task(self._ack_sender(call))
            async for event in call:
                if self._stop_event.is_set():
                    break
                received = True
                if event.revision > self._last_seen_revision:
                    self._last_seen_revision = event.revision
                kind = event.WhichOneof("event")
                if kind == "snapshot":
                    self._offer_candidate(
                        _Candidate(_clone_release(event.snapshot.release), event.revision)
                    )
                elif kind == "activation":
                    self._offer_candidate(
                        _Candidate(_clone_release(event.activation.release), event.revision)
                    )
        except asyncio.CancelledError:
            raise
        except Exception:
            pass
        finally:
            if self._watch_call is call:
                self._watch_call = None
            if sender is not None:
                sender.cancel()
                await asyncio.gather(sender, return_exceptions=True)
            if call is not None:
                try:
                    await call.done_writing()
                except Exception:
                    pass
        return received

    async def _ack_sender(self, call: Any) -> None:
        while not self._stop_event.is_set():
            await self._ack_event.wait()
            self._ack_event.clear()
            await self._flush_acks(call, replay=False)

    async def _flush_acks(self, call: Any, *, replay: bool) -> None:
        pending = sorted(
            (
                (state, generation, acknowledgement)
                for state, (generation, acknowledgement, dirty) in self._ack_latest.items()
                if replay or dirty
            ),
            key=lambda item: item[0],
        )
        for state, generation, acknowledgement in pending:
            await call.write(kms_pb2.WatchReleaseRequest(acknowledgement=acknowledgement))
            current = self._ack_latest.get(state)
            if current is not None and current[0] == generation:
                self._ack_latest[state] = (generation, current[1], False)
                self._ack_flushed_by_state[state] = generation

    async def _graceful_shutdown_after_ack(self, generation: int) -> None:
        """Flush the exact terminal ack, half-close, and drain stream EOF."""
        timeout = min(2.0, self._client._call_timeout(self._config.request_timeout))
        deadline = asyncio.get_running_loop().time() + timeout
        while self._ack_flushed_by_state.get("rejected") != generation:
            remaining = deadline - asyncio.get_running_loop().time()
            if remaining <= 0:
                break
            self._ack_event.set()
            await asyncio.sleep(min(0.01, remaining))
        self._graceful_watch_stop.set()
        self._ack_event.set()
        call = self._watch_call
        if call is not None:
            try:
                await call.done_writing()
            except Exception:
                pass
        remaining = max(0.0, deadline - asyncio.get_running_loop().time())
        try:
            await asyncio.wait_for(self._watch_done.wait(), timeout=remaining)
            return
        except asyncio.TimeoutError:
            pass
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
        divergence: Tuple[bool, int] = (False, 0),
    ) -> int:
        if state not in _STATES:
            return 0
        if state != "rejected":
            category = ""
        elif category not in _REJECTION_CATEGORIES:
            category = "internal"
        acknowledgement = kms_pb2.ReleaseAcknowledgement(
            namespace=to_proto_namespace(self._require_namespace()),
            name=self._config.name,
            version=candidate.release.version,
            activation_revision=candidate.revision,
            client_name=self._client_name,
            instance_id=self._instance_id,
            state=state,
            rejection_category=category,
            diagnostic="",
            timestamp_unix_ms=_now_ms(),
            applied_divergent=state == "applied" and divergence[0],
            divergent_field_count=divergence[1] if state == "applied" and divergence[0] else 0,
        )
        current = self._ack_latest.get(state)
        if current is None or current[1].activation_revision <= candidate.revision:
            self._ack_generation += 1
            generation = self._ack_generation
            self._ack_latest[state] = (generation, acknowledgement, True)
            self._ack_event.set()
            self._ack_counts[state] += 1
            return generation
        return 0

    def _reject(self, candidate: _Candidate, category: str) -> int:
        if category not in _REJECTION_CATEGORIES:
            category = "internal"
        self._rejection_counts[category] += 1
        if category in {
            "resolution_failed",
            "token_unavailable",
            "version_mismatch",
            "digest_mismatch",
        }:
            self._resolution_failures += 1
        self._status = replace(
            self._status,
            state="rejected",
            last_failure_category=category,
            last_failure_unix_ms=_now_ms(),
        )
        return self._ack(candidate, "rejected", category)

    def _record_resolution(self, elapsed_ms: int) -> None:
        self._resolutions += 1
        self._last_resolution_ms = max(0, elapsed_ms)
        self._status = replace(
            self._status,
            last_resolution_duration_ms=self._last_resolution_ms,
        )

    def _require_namespace(self) -> NamespaceRef:
        if self._namespace is None:
            raise errors.NoNamespaceError("release loader namespace is not resolved")
        return self._namespace


async def run_typed_release_async(
    loader: AsyncReleaseLoader,
    decode: Callable[[ReleaseSnapshot], Union[Any, Awaitable[Any]]],
    prepare: Callable[[asyncio.Event, Any], Union[PreparedRelease, Awaitable[PreparedRelease]]],
    *,
    stop_event: Optional[asyncio.Event] = None,
) -> None:
    """Run an async loader with explicit typed decoding and preparation."""

    async def prepare_snapshot(cancel: asyncio.Event, snapshot: ReleaseSnapshot) -> PreparedRelease:
        decoded = decode(snapshot)
        value = await decoded if inspect.isawaitable(decoded) else decoded
        prepared = prepare(cancel, value)
        return await prepared if inspect.isawaitable(prepared) else prepared

    await loader.run(prepare_snapshot, stop_event=stop_event)


async def _gather_until_cancel(
    tasks: list["asyncio.Task[Tuple[ReleaseEntry, Any]]"], cancel: asyncio.Event
) -> list[Tuple[ReleaseEntry, Any]]:
    cancel_task = asyncio.create_task(cancel.wait())
    gather_task = asyncio.gather(*tasks)
    try:
        wait_set: set[asyncio.Future[Any]] = {gather_task, cancel_task}
        done, _ = await asyncio.wait(wait_set, return_when=asyncio.FIRST_COMPLETED)
        if cancel_task in done and cancel.is_set():
            gather_task.cancel()
            await asyncio.gather(gather_task, return_exceptions=True)
            raise _CandidateFailure("superseded")
        return await gather_task
    except _CandidateFailure:
        raise
    except Exception as exc:
        for task in tasks:
            if not task.done():
                task.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)
        if isinstance(exc, _CandidateFailure):
            raise exc
        raise _CandidateFailure("resolution_failed") from None
    finally:
        cancel_task.cancel()
        await asyncio.gather(cancel_task, return_exceptions=True)


__all__ = [
    "AsyncManifestValidator",
    "AsyncReleaseLoader",
    "AsyncReleaseLoaderConfig",
    "AsyncSecretTokenProvider",
    "run_typed_release_async",
]
