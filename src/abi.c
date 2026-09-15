#include "abi.h"
#include <stdlib.h>
#include <string.h>
extern int zen_plugin_call(char *, uint8_t *, size_t, cliproxy_buffer *);
extern void zen_plugin_shutdown(void);
static void release_buffer(void *p, size_t n) { (void)n; free(p); }
__attribute__((visibility("default")))
int cliproxy_plugin_init(const cliproxy_host_api *host, cliproxy_plugin_api *plugin) {
 if (!host || !plugin || host->abi_version != 1) return 1;
 memset(plugin,0,sizeof(*plugin));
 plugin->abi_version=1;plugin->call=zen_plugin_call;
 plugin->free_buffer=release_buffer;plugin->shutdown=zen_plugin_shutdown;
 return 0;
}
