// Background worker: answers content-script queries. Demonstrates both a synchronous
// sendResponse and an asynchronous one (return true + a promise), which the messaging
// broker must route back to the page while keeping the page render from settling.
chrome.runtime.onMessage.addListener(function (msg, sender, sendResponse) {
  if (msg && msg.what === "getHideSelectors") {
    sendResponse({ selectors: [".bg-ad", "#bg-sponsored"] });
    return; // synchronous
  }
  if (msg && msg.what === "ping") {
    Promise.resolve().then(function () {
      sendResponse("pong");
    });
    return true; // will respond asynchronously
  }
  sendResponse(null);
});
