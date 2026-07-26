import { Html, Head, Main, NextScript } from "next/document";

export default function Document() {
  return (
    <Html lang="en">
      <Head>
        {/* public/ is copied verbatim into the static export, so the embedded
            binary answers /favicon.svg with no extra routing. */}
        <link rel="icon" href="/favicon.svg" type="image/svg+xml" />
        <meta name="theme-color" content="#0b0e14" />
      </Head>
      <body>
        <Main />
        <NextScript />
      </body>
    </Html>
  );
}
