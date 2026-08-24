// Breadcrumb trails for the console's drill-down pages. Each builder returns
// the whole trail so a page passes one array to PageHeader; the last crumb is
// the current page and renders without a link.

import type { ReactNode } from "react";
import type { IdentKind } from "@/lib/glossary";
import { links, type NamespaceRef, type ResourceRef } from "@/lib/links";

export interface Crumb {
  label?: ReactNode;
  href?: string;
  /** Rendered as a typed identifier chip instead of a text label. */
  ident?: { kind: IdentKind; value: string };
}

function applications(): Crumb[] {
  return [{ label: "Applications", href: links.applications() }];
}

function application(name: string): Crumb[] {
  return [
    ...applications(),
    { ident: { kind: "app", value: name }, href: links.application(name) },
  ];
}

function environment(ns: NamespaceRef): Crumb[] {
  return [
    ...application(ns.app),
    { ident: { kind: "env", value: ns.env }, href: links.application(ns.app, { env: ns.env }) },
  ];
}

function parameter(ref: ResourceRef): Crumb[] {
  return [
    ...environment(ref),
    { label: "Parameters", href: links.parameters({ env: ref.env, app: ref.app }) },
    { ident: { kind: "key", value: ref.key }, href: links.parameterDetail(ref) },
  ];
}

function secret(ref: ResourceRef): Crumb[] {
  return [
    ...environment(ref),
    { label: "Secrets", href: links.secrets({ env: ref.env, app: ref.app }) },
    { ident: { kind: "key", value: ref.key }, href: links.secretDetail(ref) },
  ];
}

function release(ns: NamespaceRef, name: string, version: number): Crumb[] {
  const key = `${name}@${version}`;
  return [
    ...environment(ns),
    { label: "Releases", href: links.releases({ app: ns.app, env: ns.env, name }) },
    {
      ident: { kind: "release", value: key },
      href: links.releases({ app: ns.app, env: ns.env, name, release: key }),
    },
  ];
}

export const crumbs = { applications, application, environment, parameter, secret, release };
