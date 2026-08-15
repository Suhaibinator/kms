import {
  type CallOptions,
  type ChannelCredentials,
  type ChannelOptions,
  Client,
  type ClientDuplexStream,
  credentials,
  Metadata,
} from "@grpc/grpc-js";

export interface UnaryMethod<Request, Response> {
  readonly path: string;
  readonly requestStream: false;
  readonly responseStream: false;
  readonly requestSerialize: (value: Request) => Buffer;
  readonly responseDeserialize: (value: Buffer) => Response;
}

export interface BidiMethod<Request, Response> {
  readonly path: string;
  readonly requestStream: true;
  readonly responseStream: true;
  readonly requestSerialize: (value: Request) => Buffer;
  readonly responseDeserialize: (value: Buffer) => Response;
}

export interface TransportCallOptions {
  readonly metadata?: Readonly<Record<string, string>>;
  readonly deadline?: Date;
  readonly signal?: AbortSignal;
}

export interface DuplexRpc<Request, Response> extends AsyncIterable<Response> {
  send(request: Request): Promise<void>;
  closeSend(): void;
  cancel(): void;
}

export interface RpcTransport {
  unary<Request, Response>(
    method: UnaryMethod<Request, Response>,
    request: Request,
    options?: TransportCallOptions,
  ): Promise<Response>;

  bidi<Request, Response>(
    method: BidiMethod<Request, Response>,
    options?: TransportCallOptions,
  ): DuplexRpc<Request, Response>;

  close(): void | Promise<void>;
}

export interface GrpcTransportOptions {
  readonly endpoint: string;
  readonly credentials: ChannelCredentials;
  readonly channelOptions?: ChannelOptions;
}

/** A small transport boundary that keeps reliability logic deterministic in tests. */
export class GrpcTransport implements RpcTransport {
  readonly #client: Client;
  #closed = false;

  constructor(options: GrpcTransportOptions) {
    if (!options.endpoint.trim()) {
      throw new TypeError("KMS endpoint is required");
    }
    this.#client = new Client(options.endpoint, options.credentials, options.channelOptions);
  }

  unary<Request, Response>(
    method: UnaryMethod<Request, Response>,
    request: Request,
    options: TransportCallOptions = {},
  ): Promise<Response> {
    if (this.#closed) return Promise.reject(new Error("KMS transport is closed"));
    if (options.signal?.aborted) {
      return Promise.reject(options.signal.reason ?? new DOMException("Aborted", "AbortError"));
    }

    return new Promise<Response>((resolve, reject) => {
      const callOptions: CallOptions = {};
      if (options.deadline) callOptions.deadline = options.deadline;
      let call: { cancel(): void } | undefined;
      let settled = false;
      const abort = () => call?.cancel();
      call = this.#client.makeUnaryRequest(
        method.path,
        method.requestSerialize,
        method.responseDeserialize,
        request,
        metadataFrom(options.metadata),
        callOptions,
        (error, response) => {
          settled = true;
          options.signal?.removeEventListener("abort", abort);
          if (error) reject(error);
          else if (response === undefined) reject(new Error("KMS unary RPC returned no response"));
          else resolve(response);
        },
      );
      if (!settled && options.signal) {
        options.signal.addEventListener("abort", abort, { once: true });
        // Close the race where the signal aborts after the initial check but
        // before the listener is installed.
        if (options.signal.aborted) abort();
      }
    });
  }

  bidi<Request, Response>(
    method: BidiMethod<Request, Response>,
    options: TransportCallOptions = {},
  ): DuplexRpc<Request, Response> {
    if (this.#closed) throw new Error("KMS transport is closed");
    const callOptions: CallOptions = {};
    if (options.deadline) callOptions.deadline = options.deadline;
    const stream = this.#client.makeBidiStreamRequest(
      method.path,
      method.requestSerialize,
      method.responseDeserialize,
      metadataFrom(options.metadata),
      callOptions,
    );
    return new GrpcDuplexRpc(stream, options.signal);
  }

  close(): void {
    if (this.#closed) return;
    this.#closed = true;
    this.#client.close();
  }
}

class GrpcDuplexRpc<Request, Response> implements DuplexRpc<Request, Response> {
  readonly #stream: ClientDuplexStream<Request, Response>;
  readonly #signal: AbortSignal | undefined;
  readonly #abort: (() => void) | undefined;
  #closed = false;

  constructor(stream: ClientDuplexStream<Request, Response>, signal?: AbortSignal) {
    this.#stream = stream;
    this.#signal = signal;
    if (signal) {
      this.#abort = () => this.cancel();
      if (signal.aborted) this.cancel();
      else signal.addEventListener("abort", this.#abort, { once: true });
    }
    stream.once("close", () => this.#cleanup());
    stream.once("error", () => this.#cleanup());
  }

  send(request: Request): Promise<void> {
    if (this.#closed) return Promise.reject(new Error("KMS stream is closed"));
    return new Promise<void>((resolve, reject) => {
      this.#stream.write(request, (error?: Error | null) => {
        if (error) reject(error);
        else resolve();
      });
    });
  }

  closeSend(): void {
    if (this.#closed) return;
    this.#stream.end();
  }

  cancel(): void {
    if (this.#closed) return;
    this.#closed = true;
    this.#stream.cancel();
    this.#cleanup();
  }

  [Symbol.asyncIterator](): AsyncIterator<Response> {
    const iterable = this.#stream as unknown as AsyncIterable<Response>;
    return iterable[Symbol.asyncIterator]();
  }

  #cleanup(): void {
    this.#closed = true;
    if (this.#signal && this.#abort) this.#signal.removeEventListener("abort", this.#abort);
  }
}

function metadataFrom(values?: Readonly<Record<string, string>>): Metadata {
  const metadata = new Metadata();
  if (!values) return metadata;
  for (const [key, value] of Object.entries(values)) metadata.add(key, value);
  return metadata;
}

export const insecureCredentials = (): ChannelCredentials => credentials.createInsecure();

export const tlsCredentials = (
  ca: Uint8Array,
  clientCert?: Uint8Array,
  clientKey?: Uint8Array,
): ChannelCredentials => {
  if ((clientCert === undefined) !== (clientKey === undefined)) {
    throw new TypeError("mTLS requires both a client certificate and private key");
  }
  return credentials.createSsl(
    Buffer.from(ca),
    clientKey ? Buffer.from(clientKey) : undefined,
    clientCert ? Buffer.from(clientCert) : undefined,
  );
};
