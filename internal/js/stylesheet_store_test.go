package js_test

import (
	"strings"
	"testing"
)

// A connected <style> has a sheet that stores inserted rules in order — what
// CSS-in-JS libraries in production mode write through and read back. Its
// absence was fatal to styled-components ("CSSStyleSheet could not be found on
// HTMLStyleElement"), and Next's error page replaced the app.
func TestStyleElementSheetRuleStore(t *testing.T) {
	out := renderWith(t, `<html><head></head><body><div id="out">x</div><script>
	var o = [], S = function (a) { return a.map(String).join(','); };
	var tag = document.createElement('style');
	o.push('detached=' + tag.sheet);
	document.head.appendChild(tag);
	var sh = tag.sheet;
	o.push('basic=' + S([sh instanceof CSSStyleSheet, sh === tag.sheet, sh.ownerNode === tag, sh.cssRules.length, 'sheet' in HTMLStyleElement.prototype]));
	o.push('insert=' + S([sh.insertRule('.a{color:red}', 0), sh.insertRule('.c{color:blue}', 1), sh.insertRule('.b{color:green}', 1), sh.cssRules.length]));
	o.push('order=' + S([sh.cssRules[0].cssText, sh.cssRules[1].selectorText, sh.cssRules[2].cssText]));
	sh.deleteRule(0);
	o.push('delete=' + S([sh.cssRules.length, sh.cssRules[0].selectorText, sh.rules === sh.cssRules]));
	try { sh.insertRule('.z{}', 9); o.push('bounds=ok'); } catch (e) { o.push('bounds=' + e.name); }
	try { sh.deleteRule(9); o.push('delbounds=ok'); } catch (e) { o.push('delbounds=' + e.name); }
	var other = document.createElement('style'); document.head.appendChild(other);
	o.push('separate=' + S([other.sheet !== sh, other.sheet.cssRules.length]));
	document.head.removeChild(tag);
	o.push('removed=' + tag.sheet);
	document.getElementById('out').textContent = o.join(' | ');
	</script></body></html>`, nil)
	for _, want := range []string{
		"detached=null", "basic=true,true,true,0,true", "insert=0,1,1,3",
		"order=.a{color:red},.b,.c{color:blue}", "delete=2,.b,true",
		"bounds=IndexSizeError", "delbounds=IndexSizeError", "separate=true,0", "removed=null",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if t.Failed() {
		i := strings.Index(out, `<div id="out">`)
		t.Logf("%s", out[i:min(len(out), i+500)])
	}
}
