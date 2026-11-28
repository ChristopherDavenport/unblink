const path = require('path');

// ESM output: the runtime loads chunks with a non-literal import("./" + file).
// A bundler that expands that against the filesystem (as esbuild's does) must
// never see page code: this build is the regression fixture for that.
module.exports = {
  mode: 'production',
  entry: './src/main.js',
  experiments: { outputModule: true },
  output: {
    path: path.resolve(__dirname, '../../built/webpack-esm'),
    filename: 'main.mjs',
    chunkFilename: 'chunk.[name].[contenthash:8].mjs',
    publicPath: 'auto',
    module: true,
    chunkFormat: 'module',
    chunkLoading: 'import',
    clean: true,
    uniqueName: 'unblinkEsm',
  },
  optimization: { chunkIds: 'named', moduleIds: 'named' },
  performance: { hints: false },
};
