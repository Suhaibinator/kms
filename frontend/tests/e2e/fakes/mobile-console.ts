import type { Page } from "@playwright/test";
import type { PostureResponse } from "../../../lib/types";
import { incidentState, mockConsole } from "./console-api";

export const longName = `mobile_${"long_identifier_".repeat(8)}`;

export async function mockMobileConsole(page: Page, empty = false) {
  const state = incidentState();
  if (!empty) {
    state.namespaces.prod.secrets[longName] = { key: longName, versionCount: 1, bound: false };
    state.namespaces.prod.parameters[longName] = {
      key: longName,
      content_type: "string",
      versions: [longName],
    };
  } else {
    for (const ns of Object.values(state.namespaces)) {
      ns.secrets = {};
      ns.parameters = {};
    }
  }
  await mockConsole(page, state);
  const entries: Record<string, unknown> = {
    policies: {
      policies: empty
        ? []
        : [
            {
              name: longName,
              subject: longName,
              allow: [],
              deny: [],
              created_at_unix_ms: 1,
              updated_at_unix_ms: 1,
            },
          ],
      next_page_token: "",
    },
    identities: {
      identities: empty ? [] : [{ name: longName, kind: "client", has_token: true, certs: [] }],
      next_page_token: "",
    },
    keys: {
      keys: empty
        ? []
        : [{ id: longName, source: "local", state: "active", created_at_unix_ms: 1 }],
    },
    subscribers: {
      subscribers: empty
        ? []
        : [
            {
              client_name: longName,
              instance_id: longName,
              identity: longName,
              namespaces: [{ app: "gradethis", env: "prod" }],
              remote_addr: longName,
              connected_at_unix_ms: 1,
              last_heartbeat_unix_ms: 1,
              last_acked_revision: 1,
            },
          ],
      current_revision: state.revision,
    },
    audit: {
      events: empty
        ? []
        : [
            {
              id: 1,
              event_type: "secret.read",
              actor_identity: longName,
              actor_type: "client",
              resource_type: "secret",
              resource_env: "prod",
              resource_app: "gradethis",
              resource_key: longName,
              resource_version: 1,
              resource_namespace_id: 1,
              decision: "allow",
              source_ip: "127.0.0.1",
              user_agent: longName,
              request_id: longName,
              created_at_unix_ms: 1,
              metadata_json: "{}",
            },
          ],
      next_page_token: "",
    },
    posture: mobilePosture(empty),
  };
  await page.route("**/api/v1/**", async (route) => {
    const endpoint = new URL(route.request().url()).pathname.replace("/api/v1/", "");
    if (route.request().method() === "GET" && endpoint in entries) {
      await route.fulfill({ json: entries[endpoint] });
    } else await route.fallback();
  });
  return state;
}

function mobilePosture(empty: boolean): PostureResponse {
  return {
    generated_at: "2026-09-05T00:00:00Z",
    windows: { cert: "720h0m0s", secret: "720h0m0s", admin_cert: "336h0m0s" },
    kek: {
      active_id: longName,
      created_at: "2026-01-01T00:00:00Z",
      age_seconds: 172800,
      generations: 2,
    },
    auth: { tls_enabled: true, mtls_enabled: true, admin_client_cert_required: true },
    audit: { enabled: true, retain_duration: "2160h0m0s", archive_enabled: true },
    metrics_enabled: true,
    admin_certs: { lacking: empty ? [] : [longName], expiring: [] },
    identity_certs_expiring: {
      items: empty
        ? []
        : [
            {
              identity: longName,
              env: "prod",
              app: "gradethis",
              serial: longName,
              not_after: "2026-10-01T00:00:00Z",
            },
          ],
      total: empty ? 0 : 1,
      truncated: false,
    },
    secret_versions_expiring: {
      items: empty
        ? []
        : [
            {
              env: "prod",
              app: "gradethis",
              key: longName,
              version: 1,
              expires_at: "2026-10-01T00:00:00Z",
            },
          ],
      total: empty ? 0 : 1,
      truncated: false,
    },
    changelog: { rows: 412, last_revision: 900, oldest_revision: 488 },
  };
}
