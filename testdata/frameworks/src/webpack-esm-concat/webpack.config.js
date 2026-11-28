const path = require('path');

// webpack 5.88 is pinned on purpose: its ESM chunk loader is the non-literal
// import("./" + __webpack_require__.u(chunkId)) that large sites still ship
// (newer webpack emits literal specifiers). That expression is what a
// filesystem-globbing bundler expands, so this build is the regression fixture
// for "page modules never reach esbuild's bundler". Sources are shared with
// ../webpack.
module.exports = {
  mode: 'production',
  entry: '../webpack/src/main.js',
  experiments: { outputModule: true },
  output: {
    path: path.resolve(__dirname, '../../built/webpack-esm-concat'),
    filename: 'main.mjs',
    chunkFilename: 'chunk.[name].[contenthash:8].mjs',
    publicPath: 'auto',
    module: true,
    chunkFormat: 'module',
    chunkLoading: 'import',
    clean: true,
    uniqueName: 'unblinkEsmConcat',
  },
  optimization: { chunkIds: 'named', moduleIds: 'named' },
  performance: { hints: false },
};
