package js

import "github.com/dop251/goja"

// Real extensions touch a large, quirky slice of the chrome.* surface — many niche
// namespaces (chrome.windows, chrome.webNavigation, …) and events on otherwise-real
// namespaces (chrome.tabs.onActivated, …) that unblink has no equivalent for. Rather
// than stub each by name and crash the moment one is missed (e.g. `.addListener of
// undefined`), the chrome object and each of its namespaces fall through to a *permissive
// inert value* for anything not explicitly implemented: it is callable (a method call
// resolves to undefined and invokes any trailing callback) and self-propagating (every
// property access yields the same inert, so `chrome.x.onY.addListener(fn)` no-ops). This
// keeps an extension's init running; genuinely-implemented members always win because
// they are checked first. `then` is never inert, so an inert value isn't mistaken for a
// thenable (which would hang an await).

// chromeDynamic wraps the top-level chrome object: known namespaces are returned
// permissively-wrapped (so their missing events/methods also fall through), unknown ones
// yield the inert value.
type chromeDynamic struct {
	b       *bridge
	backing *goja.Object
}

func (c chromeDynamic) Get(key string) goja.Value {
	if v := c.backing.Get(key); v != nil && !goja.IsUndefined(v) {
		if o, ok := v.(*goja.Object); ok {
			return c.b.wrapPermissive(o)
		}
		return v
	}
	if key == "then" {
		return goja.Undefined()
	}
	return c.b.inertValue()
}
func (c chromeDynamic) Set(key string, val goja.Value) bool { return c.backing.Set(key, val) == nil }
func (chromeDynamic) Has(string) bool                       { return true }
func (c chromeDynamic) Delete(key string) bool              { return c.backing.Delete(key) == nil }
func (c chromeDynamic) Keys() []string                      { return c.backing.Keys() }

func (b *bridge) wrapChrome(backing *goja.Object) *goja.Object {
	return b.vm.NewDynamicObject(chromeDynamic{b: b, backing: backing})
}

// permWrap wraps a real namespace so an unknown event/method falls through to inert.
type permWrap struct {
	b       *bridge
	backing *goja.Object
}

func (p permWrap) Get(key string) goja.Value {
	if v := p.backing.Get(key); v != nil && !goja.IsUndefined(v) {
		return v
	}
	if key == "then" {
		return goja.Undefined()
	}
	return p.b.inertValue()
}
func (p permWrap) Set(key string, val goja.Value) bool { return p.backing.Set(key, val) == nil }
func (permWrap) Has(string) bool                       { return true }
func (p permWrap) Delete(key string) bool              { return p.backing.Delete(key) == nil }
func (p permWrap) Keys() []string                      { return p.backing.Keys() }

func (b *bridge) wrapPermissive(o *goja.Object) *goja.Object {
	return b.vm.NewDynamicObject(permWrap{b: b, backing: o})
}

// inertValue returns a cached, self-propagating inert Proxy (see the package note above).
func (b *bridge) inertValue() goja.Value {
	if b.inert != nil {
		return b.inert
	}
	target := b.vm.ToValue(func(goja.FunctionCall) goja.Value { return goja.Undefined() }).ToObject(b.vm)
	emptyStr := func(goja.FunctionCall) goja.Value { return b.vm.ToValue("") }
	proxy := b.vm.NewProxy(target, &goja.ProxyTrapConfig{
		Get: func(_ *goja.Object, prop string, _ goja.Value) goja.Value {
			switch prop {
			case "then":
				return goja.Undefined() // not a thenable (avoid hanging an await)
			case "toString", "valueOf", "toJSON":
				// Coerce to a real primitive ("") so string/number contexts don't throw
				// "cannot convert object to primitive".
				return b.vm.ToValue(emptyStr)
			}
			return b.inertValue()
		},
		GetSym: func(_ *goja.Object, prop *goja.Symbol, _ goja.Value) goja.Value {
			if prop == goja.SymToPrimitive {
				return b.vm.ToValue(emptyStr)
			}
			return goja.Undefined()
		},
		Has: func(_ *goja.Object, _ string) bool { return true },
		Apply: func(_ *goja.Object, _ goja.Value, args []goja.Value) goja.Value {
			if n := len(args); n > 0 {
				if fn, ok := goja.AssertFunction(args[n-1]); ok {
					_, _ = fn(goja.Undefined(), goja.Undefined())
				}
			}
			return b.inertValue()
		},
	})
	b.inert = b.vm.ToValue(proxy)
	return b.inert
}
