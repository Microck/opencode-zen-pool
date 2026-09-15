package main

/*
#include <stdlib.h>
#include "abi.h"
*/
import "C"

import "unsafe"

var app application

//export zen_plugin_call
func zen_plugin_call(method *C.char, request *C.uint8_t, length C.size_t, out *C.cliproxy_buffer) (rc C.int) {
	if out == nil {
		return 1
	}
	out.ptr = nil
	out.len = 0
	// Keep a panic inside the plugin boundary; never print recovered values,
	// which may contain request bodies, credentials, or configuration values.
	defer func() {
		if recover() != nil {
			app.failClosed()
			b := errorEnvelope("zen_internal_error", "Zen plugin internal failure", 503)
			out.ptr = C.CBytes(b)
			out.len = C.size_t(len(b))
			rc = 0
		}
	}()
	if method == nil || uint64(length) > 64<<20 || (length > 0 && request == nil) {
		return 1
	}
	raw := C.GoBytes(unsafe.Pointer(request), C.int(length))
	b := app.call(C.GoString(method), raw)
	out.ptr = C.CBytes(b)
	out.len = C.size_t(len(b))
	return 0
}

//export zen_plugin_shutdown
func zen_plugin_shutdown() { _ = app.close() }

func main() {}
