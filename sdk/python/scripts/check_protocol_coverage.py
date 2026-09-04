#!/usr/bin/env python3
"""Fail when the wire protocol changes without an explicit Python disposition."""

from __future__ import annotations

from kms_paramstore import AsyncClient, Client
from kms_paramstore._gen import kms_pb2


CORE = {
    "kms.v1.ParameterService": {
        "GetParameter": "get_parameter",
        "PutParameter": "put_parameter",
        "ListParameters": "list_parameters",
        "DeleteParameter": "delete_parameter",
        "GetParameterMetadata": "get_parameter_metadata",
    },
    "kms.v1.SecretService": {
        "GetSecret": "get_secret",
        "PutSecret": "put_secret",
        "ListSecrets": "list_secrets",
        "DeleteSecret": "delete_secret",
        "DisableSecret": "set_secret_enabled",
        "DestroySecretVersion": "destroy_secret_version",
        "GetSecretMetadata": "get_secret_metadata",
        "PromoteSecretVersion": "promote_secret_version",
    },
    "kms.v1.AdminService": {"WhoAmI": "who_am_i"},
    "kms.v1.ConfigurationReleaseService": {
        "VerifyReleaseDefaults": "verify_release_defaults",
    },
}

CORE["kms.v1.AdminService"]["ApplyApplicationDefaults"] = "apply_application_defaults"

CLASSIFIED = {
    "kms.v1.SecretService": {
        "BindSecret": "binding-key-sdk-phase",
        "UnbindSecret": "binding-key-sdk-phase",
        "PreviewSecretBindingCohort": "binding-key-tooling-out-of-scope",
        "RotateSecretBindingKey": "binding-key-sdk-phase",
        "PurgeSecretBindingCohort": "binding-key-sdk-phase",
    },
    "kms.v1.WatchService": {"Subscribe": "internal-watch"},
    "kms.v1.ConfigurationReleaseService": {
        "CreateRelease": "release-tooling-out-of-scope",
        "ValidateRelease": "release-tooling-out-of-scope",
        "ActivateRelease": "release-tooling-out-of-scope",
        "GetRelease": "release-loader",
        "GetActiveRelease": "release-loader",
        "ListReleases": "release-tooling-out-of-scope",
        "WatchRelease": "release-loader",
    },
    "kms.v1.ConfigurationSchemaService": {
        "CreateSchema": "admin-out-of-scope",
        "GetSchema": "admin-out-of-scope",
        "ListSchemas": "admin-out-of-scope",
    },
    "kms.v1.AdminService": {
        "CreateNamespace": "admin-out-of-scope",
        "UpdateNamespace": "admin-out-of-scope",
        "DeleteNamespace": "admin-out-of-scope",
        "ListNamespaces": "admin-out-of-scope",
        "CreatePolicy": "admin-out-of-scope",
        "UpdatePolicy": "admin-out-of-scope",
        "DeletePolicy": "admin-out-of-scope",
        "ListPolicies": "admin-out-of-scope",
        "CreateIdentity": "admin-out-of-scope",
        "ListIdentities": "admin-out-of-scope",
        "RevokeIdentity": "admin-out-of-scope",
        "RotateIdentityToken": "admin-out-of-scope",
        "IssueIdentityCertificate": "admin-out-of-scope",
        "RevokeIdentityCertificate": "admin-out-of-scope",
        "GetCACertificate": "admin-out-of-scope",
        "ListAuditEvents": "admin-out-of-scope",
        "ListSubscribers": "admin-out-of-scope",
        "ListReleaseSubscribers": "admin-out-of-scope",
        "CreateApplicationRelease": "admin-out-of-scope",
        "Health": "admin-out-of-scope",
    },
}


def main() -> None:
    expected = {
        (service.full_name, method.name)
        for service in kms_pb2.DESCRIPTOR.services_by_name.values()
        for method in service.methods
    }
    covered = {
        (service, method)
        for service, methods in CORE.items()
        for method in methods
    } | {
        (service, method)
        for service, methods in CLASSIFIED.items()
        for method in methods
    }
    missing = expected - covered
    stale = covered - expected
    if missing or stale:
        raise SystemExit(f"protocol coverage drift: missing={sorted(missing)!r}, stale={sorted(stale)!r}")
    for service, methods in CORE.items():
        for rpc, python_name in methods.items():
            for client_type in (Client, AsyncClient):
                member = getattr(client_type, python_name, None)
                if member is None or not callable(member):
                    raise SystemExit(f"{client_type.__name__} does not implement {service}/{rpc} as {python_name}")
    print(f"Python protocol coverage is explicit for {len(expected)} RPCs")


if __name__ == "__main__":
    main()
