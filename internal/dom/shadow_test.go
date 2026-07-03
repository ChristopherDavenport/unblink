package dom_test

import (
	"strings"
	"testing"

	"github.com/christopherdavenport/unblink/internal/dom"
)

// Declarative Shadow DOM (<template shadowrootmode>) is inert inside a <template> on the
// no-JS path (every extraction walk skips <template>). ComposeDeclarativeShadow promotes it
// into composed light content — resolving <slot> distribution — so SSR web components render.

func TestComposeDeclarativeShadowSlots(t *testing.T) {
	doc := parse(t, `<html><body><my-card><template shadowrootmode="open"><section class="card"><h2><slot name="title"></slot></h2><div class="body"><slot></slot></div></section></template><span slot="title">Card Title</span><p>Card body text</p></my-card></body></html>`)
	dom.ComposeDeclarativeShadow(doc)
	out := renderString(t, doc)

	if strings.Contains(out, "<template") {
		t.Errorf("declarative shadow <template> not consumed:\n%s", out)
	}
	if strings.Contains(out, "<slot") {
		t.Errorf("raw <slot> leaked into composed output:\n%s", out)
	}
	if !strings.Contains(out, `<h2><span slot="title">Card Title</span></h2>`) {
		t.Errorf("named slot content not composed:\n%s", out)
	}
	if !strings.Contains(out, `<div class="body"><p>Card body text</p></div>`) {
		t.Errorf("default slot content not composed:\n%s", out)
	}
}

func TestComposeDeclarativeShadowFallback(t *testing.T) {
	doc := parse(t, `<html><body><x-fb><template shadowrootmode="open"><div><slot>fallback text</slot></div></template></x-fb></body></html>`)
	dom.ComposeDeclarativeShadow(doc)
	out := renderString(t, doc)
	if !strings.Contains(out, "<div>fallback text</div>") {
		t.Errorf("slot fallback content missing when nothing assigned:\n%s", out)
	}
}

// A document with no declarative shadow template must be left byte-for-byte unchanged.
func TestComposeDeclarativeShadowNoOp(t *testing.T) {
	doc := parse(t, `<html><body><div class="x">plain<template><span>inert</span></template></div></body></html>`)
	before := renderString(t, doc)
	dom.ComposeDeclarativeShadow(doc)
	if after := renderString(t, doc); after != before {
		t.Errorf("no-DSD document was modified:\n before: %s\n after:  %s", before, after)
	}
}

func TestComposeDeclarativeShadowNested(t *testing.T) {
	doc := parse(t, `<html><body><x-outer><template shadowrootmode="open"><div class="outer"><x-inner><template shadowrootmode="open"><span class="inner">inner content</span></template></x-inner></div></template></x-outer></body></html>`)
	dom.ComposeDeclarativeShadow(doc)
	out := renderString(t, doc)
	if strings.Contains(out, "<template") {
		t.Errorf("nested declarative shadow template not consumed:\n%s", out)
	}
	if !strings.Contains(out, `<span class="inner">inner content</span>`) {
		t.Errorf("nested shadow content not composed:\n%s", out)
	}
}
