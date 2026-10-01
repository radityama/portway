export default {
  poweredByHeader: false,
  async headers() {
    return [
      {
        source: '/:path*',
        headers: [
          { key: 'Referrer-Policy', value: 'no-referrer' },
          { key: 'X-Content-Type-Options', value: 'nosniff' },
          { key: 'X-Frame-Options', value: 'DENY' },
          {
            key: 'Content-Security-Policy',
            value: "frame-ancestors 'none'; base-uri 'self'; object-src 'none'",
          },
        ],
      },
      ...['/dashboard/:path*', '/login', '/api/:path*'].map((source) => ({
        source,
        headers: [{ key: 'Cache-Control', value: 'private, no-store' }],
      })),
    ];
  },
};
