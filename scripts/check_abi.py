#!/usr/bin/env python3
"""Load the actual native library and assert C ABI v1 / JSON schema 6 registration.
Uses temporary fake configuration only. Never makes HTTP or inference requests.
"""
import base64
import ctypes as C
import json
from pathlib import Path
import sys
import tempfile

class Buffer(C.Structure):
    _fields_ = [("ptr", C.c_void_p), ("length", C.c_size_t)]

Call = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
Free = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
Shutdown = C.CFUNCTYPE(None)

class Host(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("ctx", C.c_void_p),
                ("call", C.c_void_p), ("free", C.c_void_p)]

class Plugin(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("call", Call),
                ("free", Free), ("shutdown", Shutdown)]

def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit("usage: check_abi.py path/to/opencode-zen-pool-v0.1.0.so")
    lib = C.CDLL(str(Path(sys.argv[1]).resolve()))
    init = lib.cliproxy_plugin_init
    init.argtypes = [C.POINTER(Host), C.POINTER(Plugin)]
    init.restype = C.c_int
    host, plugin = Host(1, None, None, None), Plugin()
    assert init(C.byref(host), C.byref(plugin)) == 0
    assert plugin.abi_version == 1

    def call(method: str, data: dict) -> dict:
        request = json.dumps(data).encode()
        buf = C.create_string_buffer(request)
        out = Buffer()
        assert plugin.call(method.encode(), buf, len(request), C.byref(out)) == 0
        try:
            result = json.loads(C.string_at(out.ptr, out.length))
        finally:
            plugin.free(out.ptr, out.length)
        assert result["ok"], "native call failed (payload withheld)"
        return result.get("result", {})

    with tempfile.TemporaryDirectory(prefix="zen-abi-") as tmp:
        config = Path(tmp) / "config.yaml"
        config.write_text(json.dumps({"openai-compatibility": [{
            "name": "opencode-zen", "base-url": "https://opencode.ai/zen/v1",
            "disable-cooling": True,
            "api-key-entries": [{"api-key": "synthetic-abi-fixture-key"}],
        }]}))
        options = json.dumps({"cpa-config-path": str(config),
                              "state-dir": str(Path(tmp) / "state")}).encode()
        registration = call("plugin.register", {"schema_version": 6,
            "config_yaml": base64.b64encode(options).decode()})
        assert registration["schema_version"] == 6
        assert registration["metadata"]["Name"] == "opencode-zen-pool"
        assert registration["metadata"]["Version"] == "0.1.0"
        for cap in ("scheduler", "usage_plugin", "request_interceptor", "management_api"):
            assert registration["capabilities"][cap] is True
        call("plugin.shutdown", {})
        plugin.shutdown()
    print(json.dumps({"native_abi": 1, "schema": 6, "plugin_id": "opencode-zen-pool",
                      "version": "0.1.0", "registration": "PASS"}, indent=2))

if __name__ == "__main__":
    main()
