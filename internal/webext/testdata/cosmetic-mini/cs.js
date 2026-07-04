// Trivial content script exercising the chrome API surface: it records that it ran
// (with the extension id) and injects an element-hiding style for a dynamically-added
// class via chrome.scripting-style insertion is not used here — instead it directly
// removes a known node to prove content-script JS runs against the page DOM.
(function () {
  var marker = document.getElementById("cs-marker");
  if (marker) {
    marker.setAttribute("data-ext", chrome.runtime.id || "");
    marker.textContent = chrome.i18n.getMessage("extName");
  }
  var dyn = document.getElementById("dynamic-ad");
  if (dyn && dyn.parentNode) {
    dyn.parentNode.removeChild(dyn);
  }
})();
