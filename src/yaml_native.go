package main

/*
#cgo linux LDFLAGS: -l:libyaml-0.so.2
#include <stdlib.h>
#include "libyaml_subset.h"

// Returns one document only. Parse failures deliberately omit the input text.
static yaml_document_t *zen_yaml_load(const unsigned char *s, size_t n) {
 yaml_parser_t p;
 if (!yaml_parser_initialize(&p)) return NULL;
 yaml_parser_set_input_string(&p, s, n);
 yaml_document_t *d = calloc(1, sizeof(*d));
 if (!d) { yaml_parser_delete(&p); return NULL; }
 if (!yaml_parser_load(&p, d)) { yaml_parser_delete(&p); free(d); return NULL; }
 yaml_document_t next;
 int loaded = yaml_parser_load(&p, &next);
 int single = loaded && yaml_document_get_root_node(&next) == NULL;
 if (loaded) yaml_document_delete(&next);
 yaml_parser_delete(&p);
 if (!single) { yaml_document_delete(d); free(d); return NULL; }
 return d;
}
static void zen_yaml_free(yaml_document_t *d) { yaml_document_delete(d); free(d); }
static int zen_yaml_kind(yaml_node_t *n) { return n->type; }
static char *zen_yaml_scalar(yaml_node_t *n) { return (char *)n->data.scalar.value; }
static size_t zen_yaml_scalar_len(yaml_node_t *n) { return n->data.scalar.length; }
static size_t zen_yaml_count(yaml_node_t *n) {
 if (n->type == YAML_SEQUENCE_NODE) return n->data.sequence.items.top - n->data.sequence.items.start;
 if (n->type == YAML_MAPPING_NODE) return n->data.mapping.pairs.top - n->data.mapping.pairs.start;
 return 0;
}
static int zen_yaml_item(yaml_node_t *n, size_t i) { return n->data.sequence.items.start[i]; }
static int zen_yaml_key(yaml_node_t *n, size_t i) { return n->data.mapping.pairs.start[i].key; }
static int zen_yaml_value(yaml_node_t *n, size_t i) { return n->data.mapping.pairs.start[i].value; }
*/
import "C"

import (
	"errors"
	"strings"
	"unsafe"
)

var errYAML = errors.New("invalid YAML configuration (content withheld)")

// yamlMap uses libyaml's full YAML parser, not a line-oriented YAML subset.
// Scalars remain strings: the configuration reader resolves only typed fields
// that it owns. Aliases/merges are supported with a strict expansion budget.
func yamlMap(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	if len(raw) > 4<<20 {
		return nil, errYAML
	}
	input := C.CBytes(raw)
	defer C.free(input)
	doc := C.zen_yaml_load((*C.uchar)(input), C.size_t(len(raw)))
	if doc == nil {
		return nil, errYAML
	}
	defer C.zen_yaml_free(doc)
	root := C.yaml_document_get_root_node(doc)
	if root == nil {
		return map[string]any{}, nil
	}
	visiting := map[unsafe.Pointer]bool{}
	budget := 100000
	var decode func(*C.yaml_node_t, int) (any, error)
	decode = func(n *C.yaml_node_t, depth int) (any, error) {
		budget--
		if n == nil || depth > 64 || budget < 0 || visiting[unsafe.Pointer(n)] {
			return nil, errYAML
		}
		visiting[unsafe.Pointer(n)] = true
		defer delete(visiting, unsafe.Pointer(n))
		tag := C.GoString((*C.char)(unsafe.Pointer(n.tag)))
		if tag != "" && !strings.HasPrefix(tag, "tag:yaml.org,2002:") {
			return nil, errYAML
		}
		switch C.zen_yaml_kind(n) {
		case C.YAML_SCALAR_NODE:
			return C.GoStringN(C.zen_yaml_scalar(n), C.int(C.zen_yaml_scalar_len(n))), nil
		case C.YAML_SEQUENCE_NODE:
			a := []any{}
			for i := C.size_t(0); i < C.zen_yaml_count(n); i++ {
				v, err := decode(C.yaml_document_get_node(doc, C.zen_yaml_item(n, i)), depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			return a, nil
		case C.YAML_MAPPING_NODE:
			m := map[string]any{}
			explicit := map[string]bool{}
			var merges []map[string]any
			for i := C.size_t(0); i < C.zen_yaml_count(n); i++ {
				k, err := decode(C.yaml_document_get_node(doc, C.zen_yaml_key(n, i)), depth+1)
				if err != nil {
					return nil, err
				}
				key, ok := k.(string)
				if !ok || explicit[key] {
					return nil, errYAML
				}
				explicit[key] = true
				v, err := decode(C.yaml_document_get_node(doc, C.zen_yaml_value(n, i)), depth+1)
				if err != nil {
					return nil, err
				}
				if key != "<<" {
					m[key] = v
					continue
				}
				switch x := v.(type) {
				case map[string]any:
					merges = append(merges, x)
				case []any:
					for _, e := range x {
						mm, ok := e.(map[string]any)
						if !ok {
							return nil, errYAML
						}
						merges = append(merges, mm)
					}
				default:
					return nil, errYAML
				}
			}
			for _, mm := range merges {
				for k, v := range mm {
					if _, exists := m[k]; !exists {
						m[k] = v
					}
				}
			}
			return m, nil
		default:
			return nil, errYAML
		}
	}
	v, err := decode(root, 0)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errYAML
	}
	return m, nil
}
