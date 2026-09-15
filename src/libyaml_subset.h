/* Public parser/document declarations from libyaml 0.2.5 include/yaml.h.
 * Copyright (c) 2006-2016 Kirill Simonov. MIT, see THIRD_PARTY_NOTICES.md.
 * Unused emitter declarations and documentation omitted; no ABI changes.
 * Linux C ABI only. Pointer-only internal structures remain opaque. */
#ifndef ZEN_LIBYAML_SUBSET_H
#define ZEN_LIBYAML_SUBSET_H
#include <stddef.h>
#include <stdio.h>
typedef unsigned char yaml_char_t;
typedef struct { int major, minor; } yaml_version_directive_t;
typedef struct { yaml_char_t *handle, *prefix; } yaml_tag_directive_t;
typedef enum { YAML_ANY_ENCODING, YAML_UTF8_ENCODING, YAML_UTF16LE_ENCODING, YAML_UTF16BE_ENCODING } yaml_encoding_t;
typedef enum { YAML_NO_ERROR, YAML_MEMORY_ERROR, YAML_READER_ERROR, YAML_SCANNER_ERROR, YAML_PARSER_ERROR, YAML_COMPOSER_ERROR, YAML_WRITER_ERROR, YAML_EMITTER_ERROR } yaml_error_type_t;
typedef struct { size_t index, line, column; } yaml_mark_t;
typedef enum { YAML_ANY_SCALAR_STYLE, YAML_PLAIN_SCALAR_STYLE, YAML_SINGLE_QUOTED_SCALAR_STYLE, YAML_DOUBLE_QUOTED_SCALAR_STYLE, YAML_LITERAL_SCALAR_STYLE, YAML_FOLDED_SCALAR_STYLE } yaml_scalar_style_t;
typedef enum { YAML_ANY_SEQUENCE_STYLE, YAML_BLOCK_SEQUENCE_STYLE, YAML_FLOW_SEQUENCE_STYLE } yaml_sequence_style_t;
typedef enum { YAML_ANY_MAPPING_STYLE, YAML_BLOCK_MAPPING_STYLE, YAML_FLOW_MAPPING_STYLE } yaml_mapping_style_t;
typedef enum { YAML_NO_NODE, YAML_SCALAR_NODE, YAML_SEQUENCE_NODE, YAML_MAPPING_NODE } yaml_node_type_t;
typedef int yaml_node_item_t;
typedef struct { int key, value; } yaml_node_pair_t;
typedef struct yaml_node_s {
 yaml_node_type_t type; yaml_char_t *tag;
 union {
  struct { yaml_char_t *value; size_t length; yaml_scalar_style_t style; } scalar;
  struct { struct { yaml_node_item_t *start, *end, *top; } items; yaml_sequence_style_t style; } sequence;
  struct { struct { yaml_node_pair_t *start, *end, *top; } pairs; yaml_mapping_style_t style; } mapping;
 } data;
 yaml_mark_t start_mark, end_mark;
} yaml_node_t;
typedef struct yaml_document_s {
 struct { yaml_node_t *start, *end, *top; } nodes;
 yaml_version_directive_t *version_directive;
 struct { yaml_tag_directive_t *start, *end; } tag_directives;
 int start_implicit, end_implicit; yaml_mark_t start_mark, end_mark;
} yaml_document_t;
typedef int yaml_read_handler_t(void *, unsigned char *, size_t, size_t *);
typedef struct yaml_token_s yaml_token_t;
typedef struct yaml_simple_key_s yaml_simple_key_t;
typedef struct yaml_alias_data_s yaml_alias_data_t;
typedef enum {
 YAML_PARSE_STREAM_START_STATE, YAML_PARSE_IMPLICIT_DOCUMENT_START_STATE,
 YAML_PARSE_DOCUMENT_START_STATE, YAML_PARSE_DOCUMENT_CONTENT_STATE,
 YAML_PARSE_DOCUMENT_END_STATE, YAML_PARSE_BLOCK_NODE_STATE,
 YAML_PARSE_BLOCK_NODE_OR_INDENTLESS_SEQUENCE_STATE, YAML_PARSE_FLOW_NODE_STATE,
 YAML_PARSE_BLOCK_SEQUENCE_FIRST_ENTRY_STATE, YAML_PARSE_BLOCK_SEQUENCE_ENTRY_STATE,
 YAML_PARSE_INDENTLESS_SEQUENCE_ENTRY_STATE, YAML_PARSE_BLOCK_MAPPING_FIRST_KEY_STATE,
 YAML_PARSE_BLOCK_MAPPING_KEY_STATE, YAML_PARSE_BLOCK_MAPPING_VALUE_STATE,
 YAML_PARSE_FLOW_SEQUENCE_FIRST_ENTRY_STATE, YAML_PARSE_FLOW_SEQUENCE_ENTRY_STATE,
 YAML_PARSE_FLOW_SEQUENCE_ENTRY_MAPPING_KEY_STATE, YAML_PARSE_FLOW_SEQUENCE_ENTRY_MAPPING_VALUE_STATE,
 YAML_PARSE_FLOW_SEQUENCE_ENTRY_MAPPING_END_STATE, YAML_PARSE_FLOW_MAPPING_FIRST_KEY_STATE,
 YAML_PARSE_FLOW_MAPPING_KEY_STATE, YAML_PARSE_FLOW_MAPPING_VALUE_STATE,
 YAML_PARSE_FLOW_MAPPING_EMPTY_VALUE_STATE, YAML_PARSE_END_STATE
} yaml_parser_state_t;
typedef struct yaml_parser_s {
 yaml_error_type_t error; const char *problem; size_t problem_offset; int problem_value;
 yaml_mark_t problem_mark; const char *context; yaml_mark_t context_mark;
 yaml_read_handler_t *read_handler; void *read_handler_data;
 union { struct { const unsigned char *start, *end, *current; } string; FILE *file; } input;
 int eof;
 struct { yaml_char_t *start, *end, *pointer, *last; } buffer;
 size_t unread;
 struct { unsigned char *start, *end, *pointer, *last; } raw_buffer;
 yaml_encoding_t encoding; size_t offset; yaml_mark_t mark;
 int stream_start_produced, stream_end_produced, flow_level;
 struct { yaml_token_t *start, *end, *head, *tail; } tokens;
 size_t tokens_parsed; int token_available;
 struct { int *start, *end, *top; } indents;
 int indent, simple_key_allowed;
 struct { yaml_simple_key_t *start, *end, *top; } simple_keys;
 struct { yaml_parser_state_t *start, *end, *top; } states;
 yaml_parser_state_t state;
 struct { yaml_mark_t *start, *end, *top; } marks;
 struct { yaml_tag_directive_t *start, *end, *top; } tag_directives;
 struct { yaml_alias_data_t *start, *end, *top; } aliases;
 yaml_document_t *document;
} yaml_parser_t;
int yaml_parser_initialize(yaml_parser_t *);
void yaml_parser_delete(yaml_parser_t *);
void yaml_parser_set_input_string(yaml_parser_t *, const unsigned char *, size_t);
int yaml_parser_load(yaml_parser_t *, yaml_document_t *);
void yaml_document_delete(yaml_document_t *);
yaml_node_t *yaml_document_get_node(yaml_document_t *, int);
yaml_node_t *yaml_document_get_root_node(yaml_document_t *);
const char *yaml_get_version_string(void);
#endif
