package dom_test

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/christopherdavenport/unblink/internal/dom"
)

func renderString(t *testing.T, n *html.Node) string {
	t.Helper()
	var buf bytes.Buffer
	if err := html.Render(&buf, n); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

func TestCloneTreeRendersIdentically(t *testing.T) {
	const src = `<html><head><title>T</title><base href="/x"></head><body>
		<div id="a" class="b c" data-x="1">text<!-- comment --><p>nested <b>bold</b></p></div>
		<svg xmlns="http://www.w3.org/2000/svg"><circle r="1"></circle></svg>
		<template><span>inside template</span></template>
	</body></html>`
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	clone := dom.CloneTree(doc)
	if got, want := renderString(t, clone), renderString(t, doc); got != want {
		t.Errorf("clone renders differently\n got: %s\nwant: %s", got, want)
	}
	if clone.Parent != nil || clone.PrevSibling != nil || clone.NextSibling != nil {
		t.Error("clone must be detached")
	}
}

func TestCloneTreeIsIndependent(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<html><body><div id="a" class="x">orig</div></body></html>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	clone := dom.CloneTree(doc)
	before := renderString(t, doc)

	// Mutate the clone: attribute and text.
	var mutate func(n *html.Node)
	mutate = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" {
			n.Attr = append(n.Attr, html.Attribute{Key: "hacked", Val: "1"})
		}
		if n.Type == html.TextNode && n.Data == "orig" {
			n.Data = "changed"
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			mutate(c)
		}
	}
	mutate(clone)

	if after := renderString(t, doc); after != before {
		t.Errorf("mutating the clone changed the original\nbefore: %s\nafter:  %s", before, after)
	}
	if !strings.Contains(renderString(t, clone), "hacked") {
		t.Error("clone mutation did not apply")
	}
}
