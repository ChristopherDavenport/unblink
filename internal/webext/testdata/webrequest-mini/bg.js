// MV2 background: block tracker requests via a blocking webRequest listener — the model
// full uBlock Origin uses (its own network engine returns {cancel:true}).
chrome.webRequest.onBeforeRequest.addListener(
  function (details) {
    if (details.url.indexOf("tracker.example") !== -1) {
      return { cancel: true };
    }
    return {};
  },
  { urls: ["<all_urls>"] },
  ["blocking"]
);
