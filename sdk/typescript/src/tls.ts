import { readFileSync } from "node:fs";
import type { ChannelCredentials } from "@grpc/grpc-js";
import { tlsCredentials } from "./transport.js";

export function tlsFromFiles(caFile: string): ChannelCredentials {
  return tlsCredentials(readFileSync(caFile));
}

export function mtlsFromFiles(
  clientCertFile: string,
  clientKeyFile: string,
  caFile: string,
): ChannelCredentials {
  return tlsCredentials(readFileSync(caFile), readFileSync(clientCertFile), readFileSync(clientKeyFile));
}

export function tlsFromBytes(
  ca: Uint8Array,
  clientCert?: Uint8Array,
  clientKey?: Uint8Array,
): ChannelCredentials {
  return tlsCredentials(ca, clientCert, clientKey);
}
