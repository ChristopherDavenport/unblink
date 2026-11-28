const path = require('path');

// Classic output: <script defer> entry, the webpackChunk JSONP runtime, and a
// code-split chunk the runtime loads by injecting a <script> element.
module.exports = {
  mode: 'production',
  entry: './src/main.js',
  output: {
    path: path.resolve(__dirname, '../../built/webpack-classic'),
    filename: 'main.js',
    chunkFilename: 'chunk.[name].[contenthash:8].js',
    publicPath: '/',
    clean: true,
    uniqueName: 'unblinkClassic',
  },
  optimization: { chunkIds: 'named', moduleIds: 'named' },
  performance: { hints: false },
};
