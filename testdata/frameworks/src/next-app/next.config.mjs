// Static export of an App Router site: prerendered HTML plus the Flight (RSC)
// payload inlined as a stream and as .txt files for client navigation.
/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'export',
  trailingSlash: true,
  // A fixed build id keeps the exported file names stable across rebuilds.
  generateBuildId: async () => 'unblink-fixture',
  productionBrowserSourceMaps: false,
  poweredByHeader: false,
};
export default nextConfig;
