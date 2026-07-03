from __future__ import annotations

import pytest

from kms_paramstore._refs import (
    NamespaceRef,
    Ref,
    parse_namespace,
    split_display_path,
    split_display_pattern,
)
from kms_paramstore.errors import ConfigError


def test_split_display_pattern_requires_third_segment():
    # Whole namespace is spelled with an explicit "*"...
    ns, pat = split_display_pattern("/prod/app/*")
    assert ns == NamespaceRef("prod", "app") and pat == "*"
    # ...a subtree keeps its pattern...
    ns, pat = split_display_pattern("/prod/app/billing/*")
    assert (ns, pat) == (NamespaceRef("prod", "app"), "billing/*")
    # ...and a missing 3rd segment is an error (Go parity; not silently "*").
    with pytest.raises(ConfigError):
        split_display_pattern("/prod/app")
    with pytest.raises(ConfigError):
        split_display_pattern("prod/app/*")  # must start with "/"


def test_parse_namespace_structural_only():
    assert parse_namespace("prod/app") == NamespaceRef("prod", "app")
    # Character set is the server's authority: an uppercase name the SDK cannot
    # prove wrong is accepted client-side (matches the Go SDK).
    assert parse_namespace("Prod/App") == NamespaceRef("Prod", "App")
    for bad in ("noslash", "prod/", "/app", "prod/a/b"):
        with pytest.raises(ConfigError):
            parse_namespace(bad)


def test_split_display_path_structural_only():
    r = split_display_path("/prod/app/billing/stripe-key")
    assert r == Ref(NamespaceRef("prod", "app"), "billing/stripe-key")
    assert str(r) == "/prod/app/billing/stripe-key"
    # Uppercase labels/keys accepted (server authoritative on charset).
    assert split_display_path("/Prod/App/KEY") == Ref(NamespaceRef("Prod", "App"), "KEY")
    for bad in ("prod/app/key", "/prod/app", "/prod//key", "/prod/app/"):
        with pytest.raises(ConfigError):
            split_display_path(bad)
