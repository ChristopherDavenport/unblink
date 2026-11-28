// Loaded with <script defer>: runs after the document is parsed and after every
// inline classic script, but before DOMContentLoaded.
window.__order.push('defer');
window.__deferSawTail = window.__order.indexOf('inline-tail') >= 0;
