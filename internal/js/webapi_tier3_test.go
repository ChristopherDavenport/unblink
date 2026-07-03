package js_test

import (
	"strings"
	"testing"
	"time"
)

// --- Batch L: media & Web Audio crash-avoidance stubs ---

// TestMediaAudioStubs proves video.play()/pause()/load(), new Audio(),
// MediaSource, and the Web Audio graph all construct and run inert instead of
// throwing. Content is written from the nested play()->resume() completion.
func TestMediaAudioStubs(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var r = [];
		  var v = document.createElement('video');
		  v.pause(); v.load();
		  r.push('canPlay=[' + v.canPlayType('video/mp4') + ']');
		  var a = new Audio('/x.mp3');
		  r.push('audio=' + a.tagName + ',' + (a.src.indexOf('/x.mp3') >= 0));
		  r.push('msSupported=' + MediaSource.isTypeSupported('video/mp4'));
		  var ctx = new AudioContext();
		  var osc = ctx.createOscillator();
		  osc.frequency.value = 220;
		  osc.connect(ctx.destination); osc.start(); osc.stop();
		  r.push('audioCtx=' + ctx.state + ',bins=' + ctx.createAnalyser().frequencyBinCount);
		  v.play().then(function () {
		    r.push('play=ok');
		    ctx.resume().then(function () {
		      r.push('resumed=' + ctx.state);
		      document.getElementById('out').textContent = r.join(' ');
		    });
		  });
		</script></body></html>`)
	for _, want := range []string{
		"canPlay=[]", "audio=AUDIO,true", "msSupported=false",
		"audioCtx=suspended,bins=1024", "play=ok", "resumed=running",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("media/audio stubs missing %q:\n%s", want, out)
		}
	}
}

// --- Batch M: navigator device APIs + niche constructors ---

// TestNavigatorDeviceStubs proves the niche device surfaces construct/resolve/
// reject inertly (Notification permission denied, battery/locks resolve, bluetooth
// rejects) rather than throwing. Content is written from the end of the chain.
func TestNavigatorDeviceStubs(t *testing.T) {
	out := render(t, `<html><body><div id="out">?</div>
		<script>
		  var r = [];
		  r.push('gamepads=' + navigator.getGamepads().length);
		  r.push('notif=' + Notification.permission);
		  r.push('rtc=' + (typeof new RTCPeerConnection().createOffer === 'function'));
		  r.push('speech=' + (typeof speechSynthesis.getVoices === 'function') + ',' + speechSynthesis.getVoices().length);
		  r.push('gyro=' + (typeof new Gyroscope().start === 'function'));
		  r.push('pay=' + (typeof new PaymentRequest([], {}).show === 'function'));
		  navigator.getBattery().then(function (b) {
		    r.push('battery=' + b.level + ',' + b.charging);
		    return navigator.locks.request('lk', function () { return 'held'; });
		  }).then(function (lockResult) {
		    r.push('lock=' + lockResult);
		    return navigator.bluetooth.requestDevice().then(function () { return 'resolved'; }, function () { return 'rejected'; });
		  }).then(function (bt) {
		    r.push('bt=' + bt);
		    return Notification.requestPermission();
		  }).then(function (perm) {
		    r.push('perm=' + perm);
		    document.getElementById('out').textContent = r.join(' ');
		  });
		</script></body></html>`)
	for _, want := range []string{
		"gamepads=0", "notif=denied", "rtc=true", "speech=true,0", "gyro=true",
		"pay=true", "battery=1,true", "lock=held", "bt=rejected", "perm=denied",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("navigator device stubs missing %q:\n%s", want, out)
		}
	}
}

// --- Batch N: element/document interaction stubs ---

// TestElementDocumentStubs proves popover/fullscreen/PiP no-ops don't throw,
// ToggleEvent constructs, startViewTransition runs its callback (so the router's
// new view materializes), and the serviceWorker registration exposes inert
// sync/push. Content is written from the end of the chain (settle proof).
func TestElementDocumentStubs(t *testing.T) {
	out := render(t, `<html><body>
		<div id="pop" popover>popover body content</div>
		<div id="out">?</div>
		<script>
		  var r = [];
		  var pop = document.getElementById('pop');
		  pop.showPopover(); pop.hidePopover();
		  r.push('toggle=' + pop.togglePopover(true));
		  r.push('fsEnabled=' + document.fullscreenEnabled + ',fsEl=' + (document.fullscreenElement === null));
		  r.push('pip=' + document.pictureInPictureEnabled);
		  r.push('toggleEv=' + new ToggleEvent('toggle', { newState: 'open' }).newState);
		  document.body.requestFullscreen().then(function () {
		    r.push('fs=ok');
		    var vt = document.startViewTransition(function () { document.getElementById('pop').textContent = 'transitioned'; });
		    vt.finished.then(function () {
		      r.push('vt=' + document.getElementById('pop').textContent);
		      return navigator.serviceWorker.register('/sw.js');
		    }).then(function (reg) {
		      return reg.pushManager.getSubscription();
		    }).then(function (sub) {
		      r.push('push=' + (sub === null));
		      document.getElementById('out').textContent = r.join(' ');
		    });
		  });
		</script></body></html>`)
	for _, want := range []string{
		"toggle=true", "fsEnabled=true,fsEl=true", "pip=false", "toggleEv=open",
		"fs=ok", "vt=transitioned", "push=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("element/document stubs missing %q:\n%s", want, out)
		}
	}
}

// TestTier3LiveContext confirms the Tier 3 stubs are installed in the persistent
// (interact/session) runtime too, not just one-shot Render.
func TestTier3LiveContext(t *testing.T) {
	lc, cleanup := openContext(t, `<html><body><div id="out">?</div>
		<script>
		  document.getElementById('out').textContent = [
		    'play=' + (typeof document.createElement('video').play === 'function'),
		    'audio=' + (typeof AudioContext === 'function'),
		    'rtc=' + (typeof RTCPeerConnection === 'function'),
		    'notif=' + (Notification.permission === 'denied'),
		    'battery=' + (typeof navigator.getBattery === 'function'),
		    'popover=' + (typeof document.body.showPopover === 'function'),
		    'viewTransition=' + (typeof document.startViewTransition === 'function'),
		    'gpu=' + (typeof navigator.gpu.requestAdapter === 'function')
		  ].join(' ');
		</script></body></html>`, 2*time.Second)
	defer cleanup()
	snap := snapshot(t, lc)
	for _, want := range []string{
		"play=true", "audio=true", "rtc=true", "notif=true", "battery=true",
		"popover=true", "viewTransition=true", "gpu=true",
	} {
		if !strings.Contains(snap, want) {
			t.Errorf("Tier 3 live context missing %q:\n%s", want, snap)
		}
	}
}
