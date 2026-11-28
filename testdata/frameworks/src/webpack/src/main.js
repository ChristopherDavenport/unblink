// A small vanilla app: the article shell renders synchronously, the almanac
// section arrives through webpack's chunk-loading runtime.
const root = document.getElementById('root');
root.innerHTML =
  '<article><h1>Webpack Almanac Shell</h1>' +
  '<p>A production webpack 5 build rendered this article; the section below is a code-split chunk loaded by the webpack runtime.</p>' +
  '<section id="almanac"><p>almanac pending</p></section></article>';

import(/* webpackChunkName: "almanac" */ './almanac.js').then((m) => {
  document.getElementById('almanac').innerHTML = m.render();
});

// Present in the build, never called: its chunk must not be requested.
window.loadGazette = () => import(/* webpackChunkName: "gazette" */ './gazette.js');
