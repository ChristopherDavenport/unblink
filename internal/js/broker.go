package js

// msgBroker routes chrome.runtime messages between page runtimes and the background
// runtime. Message payloads are plain Go values (goja values cannot cross runtimes);
// delivery happens via each target loop's RunOnLoop. Phase 3 implements the
// page → background path (a content script asking the background, e.g. for the cosmetic
// filters that apply to the current host); background → page broadcast is later.
type msgBroker struct {
	host *ExtensionHost
}

// sendToBackground delivers msg to the background's onMessage listeners and routes the
// reply through respond. respond is always invoked exactly once (nil when there is no
// background or no responding listener), so the caller's pending bracket is released.
func (br *msgBroker) sendToBackground(msg, sender any, respond func(any)) {
	if br == nil || br.host == nil || br.host.bg == nil {
		respond(nil)
		return
	}
	br.host.bg.deliver(msg, sender, respond)
}
