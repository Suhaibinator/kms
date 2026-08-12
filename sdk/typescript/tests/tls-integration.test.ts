import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  type handleUnaryCall,
  Server,
  ServerCredentials,
  type UntypedServiceImplementation,
} from "@grpc/grpc-js";
import { describe, expect, it } from "vitest";
import {
  AdminServiceService,
  type WhoAmIRequest,
  type WhoAmIResponse,
} from "../src/generated/kms.js";
import { KmsClient, KmsError, mtlsFromFiles, tlsFromBytes, tlsFromFiles } from "../src/index.js";

describe("gRPC TLS integration", () => {
  it("authenticates a bearer client over server-authenticated TLS", async () => {
    const directory = mkdtempSync(join(tmpdir(), "kms-sdk-tls-"));
    const paths = createCertificateFixture(directory);
    const server = new Server();
    let calls = 0;
    const whoAmI: handleUnaryCall<WhoAmIRequest, WhoAmIResponse> = (call, callback) => {
      calls += 1;
      expect(call.metadata.get("authorization")).toEqual(["Bearer server-auth-token"]);
      callback(null, {
        name: "sdk-token-client",
        kind: "client",
        namespace: { env: "prod", app: "api" },
        authMethod: "token",
      });
    };
    server.addService(AdminServiceService, {
      whoAmI,
    } as UntypedServiceImplementation);

    const credentials = ServerCredentials.createSsl(
      readFileSync(paths.caCert),
      [
        {
          private_key: readFileSync(paths.serverKey),
          cert_chain: readFileSync(paths.serverCert),
        },
      ],
      false,
    );
    const port = await bind(server, credentials);
    const client = new KmsClient({
      endpoint: `127.0.0.1:${port}`,
      token: "server-auth-token",
      credentials: tlsFromFiles(paths.caCert),
      timeoutMs: 2_000,
      channelOptions: localTlsChannelOptions(),
    });

    try {
      await expect(client.whoAmI()).resolves.toEqual({
        identity: "sdk-token-client",
        kind: "client",
        namespace: "prod/api",
        authMethod: "token",
      });
      expect(calls).toBe(1);
    } finally {
      await client.close();
      await shutdown(server);
      rmSync(directory, { recursive: true, force: true });
    }
  }, 15_000);

  it("authenticates a public client over mTLS and rejects a client without a certificate", async () => {
    const directory = mkdtempSync(join(tmpdir(), "kms-sdk-mtls-"));
    const paths = createCertificateFixture(directory);
    const ca = readFileSync(paths.caCert);
    const server = new Server();
    let calls = 0;
    const whoAmI: handleUnaryCall<WhoAmIRequest, WhoAmIResponse> = (call, callback) => {
      calls += 1;
      expect(call.metadata.get("authorization")).toEqual(["Bearer integration-token"]);
      callback(null, {
        name: "sdk-integration",
        kind: "client",
        namespace: { env: "prod", app: "api" },
        authMethod: "mtls",
      });
    };
    server.addService(AdminServiceService, {
      whoAmI,
    } as UntypedServiceImplementation);

    const credentials = ServerCredentials.createSsl(
      ca,
      [
        {
          private_key: readFileSync(paths.serverKey),
          cert_chain: readFileSync(paths.serverCert),
        },
      ],
      true,
    );
    const port = await bind(server, credentials);
    const endpoint = `127.0.0.1:${port}`;
    const client = new KmsClient({
      endpoint,
      token: "integration-token",
      credentials: mtlsFromFiles(paths.clientCert, paths.clientKey, paths.caCert),
      timeoutMs: 2_000,
      channelOptions: localTlsChannelOptions(),
    });
    const certificateLessClient = new KmsClient({
      endpoint,
      credentials: tlsFromBytes(ca),
      timeoutMs: 250,
      channelOptions: {
        ...localTlsChannelOptions(),
        "grpc.initial_reconnect_backoff_ms": 25,
        "grpc.max_reconnect_backoff_ms": 25,
      },
    });

    try {
      await expect(client.whoAmI()).resolves.toEqual({
        identity: "sdk-integration",
        kind: "client",
        namespace: "prod/api",
        authMethod: "mtls",
      });
      await expect(certificateLessClient.whoAmI()).rejects.toBeInstanceOf(KmsError);
      expect(calls).toBe(1);
    } finally {
      await Promise.allSettled([client.close(), certificateLessClient.close()]);
      await shutdown(server);
      rmSync(directory, { recursive: true, force: true });
    }
  }, 15_000);
});

interface CertificatePaths {
  readonly caCert: string;
  readonly clientCert: string;
  readonly clientKey: string;
  readonly serverCert: string;
  readonly serverKey: string;
}

function createCertificateFixture(directory: string): CertificatePaths {
  const caCert = join(directory, "ca.crt");
  const caKey = join(directory, "ca.key");
  const serverCert = join(directory, "server.crt");
  const serverKey = join(directory, "server.key");
  const serverRequest = join(directory, "server.csr");
  const serverExtensions = join(directory, "server.ext");
  const clientCert = join(directory, "client.crt");
  const clientKey = join(directory, "client.key");
  const clientRequest = join(directory, "client.csr");
  const clientExtensions = join(directory, "client.ext");

  openssl([
    "req",
    "-x509",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-keyout",
    caKey,
    "-out",
    caCert,
    "-days",
    "1",
    "-sha256",
    "-subj",
    "/CN=KMS SDK Test CA",
  ]);
  writeFileSync(
    serverExtensions,
    "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\nkeyUsage=digitalSignature,keyEncipherment\n",
  );
  openssl([
    "req",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-keyout",
    serverKey,
    "-out",
    serverRequest,
    "-subj",
    "/CN=localhost",
  ]);
  openssl([
    "x509",
    "-req",
    "-in",
    serverRequest,
    "-CA",
    caCert,
    "-CAkey",
    caKey,
    "-CAcreateserial",
    "-out",
    serverCert,
    "-days",
    "1",
    "-sha256",
    "-extfile",
    serverExtensions,
  ]);
  writeFileSync(
    clientExtensions,
    "extendedKeyUsage=clientAuth\nkeyUsage=digitalSignature,keyEncipherment\n",
  );
  openssl([
    "req",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-keyout",
    clientKey,
    "-out",
    clientRequest,
    "-subj",
    "/CN=sdk-integration",
  ]);
  openssl([
    "x509",
    "-req",
    "-in",
    clientRequest,
    "-CA",
    caCert,
    "-CAkey",
    caKey,
    "-CAserial",
    join(directory, "ca.srl"),
    "-out",
    clientCert,
    "-days",
    "1",
    "-sha256",
    "-extfile",
    clientExtensions,
  ]);

  return { caCert, clientCert, clientKey, serverCert, serverKey };
}

function openssl(args: readonly string[]): void {
  execFileSync("openssl", args, { stdio: "ignore" });
}

function localTlsChannelOptions(): Readonly<Record<string, string>> {
  return {
    "grpc.ssl_target_name_override": "localhost",
    "grpc.default_authority": "localhost",
  };
}

function bind(server: Server, credentials: ServerCredentials): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    server.bindAsync("127.0.0.1:0", credentials, (error, port) => {
      if (error) reject(error);
      else resolve(port);
    });
  });
}

function shutdown(server: Server): Promise<void> {
  return new Promise<void>((resolve) => {
    server.tryShutdown((error) => {
      if (error) server.forceShutdown();
      resolve();
    });
  });
}
