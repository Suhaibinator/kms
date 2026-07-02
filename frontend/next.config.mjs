// @ts-check

/**
 * The frontend ships as a fully static export embedded into the Go binary.
 * No server runtime is available at deploy time: every page is statically
 * prerendered and all dynamic data is fetched client-side from /api/v1/*.
 *
 * In development we proxy /api to the local Go server. `rewrites` are ignored
 * by `output: "export"` at build time, so we only attach them when running
 * `next dev` (NODE_ENV !== "production") to avoid a build-time warning.
 */

const isDev = process.env.NODE_ENV !== "production";

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "export",
  reactStrictMode: true,
  trailingSlash: false,
  // Static export cannot use the Next.js image optimization runtime.
  images: {
    unoptimized: true,
  },
  ...(isDev
    ? {
        async rewrites() {
          return [
            {
              source: "/api/:path*",
              destination: "http://localhost:8080/api/:path*",
            },
          ];
        },
      }
    : {}),
};

export default nextConfig;
