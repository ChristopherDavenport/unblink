package js

// throwDOMException panics with a real DOMException (the JS global defined in
// preludeJS) so testharness's assert_throws_dom, `instanceof DOMException`, and the
// legacy `.code`/`.name` all work. Callers must be on the loop goroutine (like any
// goja throw). Falls back to a TypeError if the global is somehow unavailable.
func (b *bridge) throwDOMException(name, message string) {
	if ctor := b.vm.Get("DOMException"); ctor != nil {
		if obj, err := b.vm.New(ctor, b.vm.ToValue(message), b.vm.ToValue(name)); err == nil {
			panic(obj)
		}
	}
	panic(b.vm.NewTypeError("%s: %s", name, message))
}
