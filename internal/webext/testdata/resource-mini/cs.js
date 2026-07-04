// Content script: fetch a bundled extension resource via chrome.runtime.getURL — the
// chrome-extension:// URL must resolve to the packaged file, not the network.
fetch(chrome.runtime.getURL("data.json"))
  .then(function (r) {
    return r.json();
  })
  .then(function (d) {
    var el = document.getElementById("res");
    if (el) {
      el.textContent = d.msg;
    }
  });
