import { expect, it } from "vitest";
import { ConfigurationSchema } from "../src/generated/kms.js";

it("distinguishes unestablished and established-empty contracts in the Go server wire format", () => {
  const unknown = ConfigurationSchema.decode(new Uint8Array());
  // Produced by toProtoConfigurationSchema with an established empty contract.
  const wire = Uint8Array.of(0x50, 0x01);
  const established = ConfigurationSchema.decode(wire);

  expect(unknown.contract).toEqual([]);
  expect(unknown.contractEstablished).toBe(false);
  expect(established.contract).toEqual([]);
  expect(established.contractEstablished).toBe(true);
  expect(ConfigurationSchema.encode(established).finish()).toEqual(wire);
});
