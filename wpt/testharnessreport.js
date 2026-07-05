/*
 * unblink WPT reporter. Served in place of upstream /resources/testharnessreport.js.
 *
 * The unblink JS engine has no value-returning eval; the only DOM readback is the
 * serialized tree. So instead of the upstream reporter's cross-document channel
 * (postMessage to window.opener), we register a completion callback that serializes
 * every subtest result INTO the DOM as a JSON <script> the Go harness recovers by
 * walking the node tree for id="__wpt_results". add_completion_callback fires after
 * every sync/async/promise subtest finishes, so results are final when it runs; the
 * harness's Wait gate on #__wpt_results holds the render until this appends the node.
 */
(function () {
  "use strict";
  if (typeof add_completion_callback !== "function") {
    return; // testharness.js failed to load; harness sees a missing results node.
  }
  // Shorten testharness's own harness timeout so a test hung on an unsupported
  // capability self-completes (marking unfinished subtests TIMEOUT and firing the
  // completion callback below) well before the engine's render budget, instead of
  // burning the whole budget with no results node. setup() reschedules the already
  // armed timer. 0.35 → normal 10s becomes 3.5s, comfortably under the 5s budget;
  // content-extraction subtests settle in well under that, so false timeouts are
  // rare and, being excluded from the denominator, never inflate conformance.
  try {
    if (typeof setup === "function") {
      setup({ timeout_multiplier: 0.35 });
    }
  } catch (e) {
    /* a test may reconfigure setup itself; the render budget remains the backstop */
  }
  function emit(obj) {
    var s = document.createElement("script");
    s.type = "application/json";
    s.id = "__wpt_results";
    // <script> text is serialized verbatim; a "</script" inside a message would end
    // the element early, so neutralize it before it reaches the serializer.
    s.textContent = JSON.stringify(obj).replace(/<\/script/gi, "<\\/script");
    (document.body || document.documentElement || document).appendChild(s);
  }
  add_completion_callback(function (tests, status) {
    try {
      emit({
        // harness_status: 0 OK, 1 ERROR, 2 TIMEOUT, 3 PRECONDITION_FAILED.
        harness_status: status ? status.status : -1,
        harness_message: status && status.message ? String(status.message) : "",
        // subtest status: 0 PASS, 1 FAIL, 2 TIMEOUT, 3 NOTRUN, 4 PRECONDITION_FAILED.
        tests: (tests || []).map(function (t) {
          return {
            name: String(t.name),
            status: t.status,
            message: t.message ? String(t.message) : ""
          };
        })
      });
    } catch (e) {
      emit({ harness_status: -2, harness_message: String(e), tests: [] });
    }
  });
})();
