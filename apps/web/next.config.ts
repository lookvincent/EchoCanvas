import type { NextConfig } from "next";

const gatewayTarget = process.env.GO_GATEWAY_URL || "http://127.0.0.1:8080";

const nextConfig: NextConfig = {
  images: {
    unoptimized: true,
  },
  async rewrites() {
    return [
      {
        source: "/api/v1/:path*",
        destination: `${gatewayTarget}/api/v1/:path*`,
      },
    ];
  },
};

export default nextConfig;
