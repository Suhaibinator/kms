import { Head, Html, Main, NextScript } from "next/document";
import { THEME_BOOT_SCRIPT, THEME_COLOR } from "@/lib/theme";

export default function Document() {
  return (
    // No theme class here: the inline script below decides before first paint,
    // from the stored preference or the OS setting, so a dark-mode visitor
    // never sees a light flash (or vice versa). React does not manage <html>,
    // so the class it sets is not a hydration concern.
    <Html lang="en">
      <Head>
        {/* public/ is copied verbatim into the static export, so the embedded
            binary answers /favicon.svg with no extra routing. */}
        <link rel="icon" href="/favicon.svg" type="image/svg+xml" />
        <meta name="theme-color" content={THEME_COLOR.light} />
        {/* biome-ignore lint/security/noDangerouslySetInnerHtml: constant, self-contained, built from our own literals */}
        <script dangerouslySetInnerHTML={{ __html: THEME_BOOT_SCRIPT }} />
      </Head>
      <body>
        <Main />
        <NextScript />
      </body>
    </Html>
  );
}
