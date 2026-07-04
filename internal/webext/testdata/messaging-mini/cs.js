// Content script: asks the background which elements to hide (dynamic cosmetic
// filtering, uBlock's model), removes them, and records the async ping reply — proving
// the message round-trip lands before the render settles.
chrome.runtime.sendMessage({ what: "getHideSelectors" }, function (resp) {
  if (resp && resp.selectors) {
    for (var i = 0; i < resp.selectors.length; i++) {
      var list = document.querySelectorAll(resp.selectors[i]);
      for (var j = 0; j < list.length; j++) {
        list[j].remove();
      }
    }
  }
});

chrome.runtime.sendMessage({ what: "ping" }).then(function (r) {
  var m = document.getElementById("ping-result");
  if (m) {
    m.textContent = r;
  }
});
