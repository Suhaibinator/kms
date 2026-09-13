"""Real-server driver for TestReleaseSDKConformance (sync and async Python).

The host harness provisions a release, severs the TCP proxy, and inspects server
projections while this process stays alive. No mocks are used here.
"""
import argparse
import asyncio
import os
from pathlib import Path
import signal
import threading

import grpc

from kms_paramstore import Client, ReleaseLoader, ReleaseLoaderConfig
from kms_paramstore.async_client import AsyncClient
from kms_paramstore.async_release import AsyncReleaseLoader, AsyncReleaseLoaderConfig


class Prepared:
    def commit(self):
        pass

    def abort(self):
        pass


def prepare(_cancel, snapshot):
    # Access the resolved parameter so the test exercises actual resolution,
    # preparation, commit, and acknowledgement rather than just a watch stream.
    assert "setting" in snapshot.parameters
    return Prepared()


def options(mode):
    return dict(
        endpoint=os.environ["KMS_CONFORMANCE_ENDPOINT"],
        token=os.environ["KMS_CONFORMANCE_TOKEN"],
        namespace=os.environ["KMS_CONFORMANCE_NAMESPACE"],
        tls=grpc.ssl_channel_credentials(
            root_certificates=Path(os.environ["KMS_CONFORMANCE_CA_FILE"]).read_bytes()),
        client_name=f"python-{mode}",
        channel_options=[("grpc.ssl_target_name_override", "localhost")],
    )


def config():
    return dict(
        name=os.environ["KMS_CONFORMANCE_RELEASE"],
        schema_version=int(os.environ["KMS_CONFORMANCE_SCHEMA_VERSION"]),
        instance_id=os.environ["KMS_CONFORMANCE_INSTANCE"],
        reconnect_initial=0.05,
        reconnect_max=0.1,
        reconcile_interval=0.2,
    )


def run_sync():
    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    with Client(**options("sync")) as client:
        ReleaseLoader(client, ReleaseLoaderConfig(**config())).run(prepare, stop_event=stop)


async def run_async():
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop.set)
    client = AsyncClient(**options("async"))
    try:
        await AsyncReleaseLoader(client, AsyncReleaseLoaderConfig(**config())).run(
            prepare, stop_event=stop)
    finally:
        await client.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("sync", "async"))
    mode = parser.parse_args().mode
    if mode == "sync":
        run_sync()
    else:
        asyncio.run(run_async())
