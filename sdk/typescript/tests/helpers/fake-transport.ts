import type {
  BidiMethod,
  DuplexRpc,
  RpcTransport,
  TransportCallOptions,
  UnaryMethod,
} from "../../src/transport.js";

export interface UnaryCall {
  readonly path: string;
  readonly request: unknown;
  readonly options: TransportCallOptions;
}

export class FakeTransport implements RpcTransport {
  readonly calls: UnaryCall[] = [];
  readonly streams: FakeDuplex<unknown, unknown>[] = [];
  closeCount = 0;

  constructor(
    readonly handler: (
      path: string,
      request: unknown,
      options: TransportCallOptions,
    ) => unknown | Promise<unknown>,
  ) {}

  async unary<Request, Response>(
    method: UnaryMethod<Request, Response>,
    request: Request,
    options: TransportCallOptions = {},
  ): Promise<Response> {
    this.calls.push({ path: method.path, request, options });
    return (await this.handler(method.path, request, options)) as Response;
  }

  bidi<Request, Response>(
    _method: BidiMethod<Request, Response>,
    options: TransportCallOptions = {},
  ): DuplexRpc<Request, Response> {
    const stream = new FakeDuplex<Request, Response>();
    this.streams.push(stream as FakeDuplex<unknown, unknown>);
    if (options.signal) {
      if (options.signal.aborted) stream.cancel();
      else options.signal.addEventListener("abort", () => stream.cancel(), { once: true });
    }
    return stream;
  }

  close(): void {
    this.closeCount++;
    for (const stream of this.streams) stream.cancel();
  }
}

export class FakeDuplex<Request, Response> implements DuplexRpc<Request, Response> {
  readonly sent: Request[] = [];
  readonly #responses: Response[] = [];
  readonly #waiters: ((result: IteratorResult<Response>) => void)[] = [];
  #closed = false;

  async send(request: Request): Promise<void> {
    if (this.#closed) throw new Error("stream closed");
    this.sent.push(request);
  }

  emit(response: Response): void {
    if (this.#closed) return;
    const waiter = this.#waiters.shift();
    if (waiter) waiter({ done: false, value: response });
    else this.#responses.push(response);
  }

  closeSend(): void {
    this.cancel();
  }

  cancel(): void {
    if (this.#closed) return;
    this.#closed = true;
    for (const waiter of this.#waiters.splice(0)) waiter({ done: true, value: undefined });
  }

  [Symbol.asyncIterator](): AsyncIterator<Response> {
    return {
      next: () => {
        const response = this.#responses.shift();
        if (response !== undefined) return Promise.resolve({ done: false, value: response });
        if (this.#closed) return Promise.resolve({ done: true, value: undefined });
        return new Promise<IteratorResult<Response>>((resolve) => this.#waiters.push(resolve));
      },
      return: () => {
        this.cancel();
        return Promise.resolve({ done: true, value: undefined });
      },
    };
  }
}

export async function waitFor(predicate: () => boolean, timeoutMs = 1_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error("timed out waiting for condition");
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
}
