import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Proxy API calls to the Go server so the session cookie stays same-origin.
  async rewrites() {
    return [
      {
        source: "/api/:path*",
        destination: "http://localhost:8080/api/:path*",
      },
    ];
  },
};

export default nextConfig;
