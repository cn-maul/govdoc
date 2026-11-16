package llm

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cn-maul/rosetta"
)

// planResponseFormat asks the provider to constrain the answer to the plan's
// shape.
//
// # Why this is worth pinning
//
// The prompt already specifies the JSON by example, and a capable model
// follows it. The failures seen in practice are not misses of intent but of
// syntax: a trailing comma, a fence despite being told not to, a section id
// typed as a number. Each one costs a whole repair round — a second model
// call, tens of seconds, and the user's tokens — to fix something the
// provider can enforce for free.
//
// A schema also rules out the two structural mistakes VerifyPlan would
// otherwise have to catch a round later: an id that is not a legal node
// reference, and a level outside 1–4. Rejecting those at generation time is
// strictly better than reporting them afterwards, because the model never
// gets the chance to build a tree around them.
//
// # Why json_schema and not json_object
//
// json_object only guarantees "some object"; it would not constrain section
// ids at all. json_schema is supported by OpenAI, Anthropic (via
// output_config.format) and most compatible gateways; where it is not, chat()
// retries without it (see rejectsResponseFormat).
func planResponseFormat() *rosetta.ResponseFormat {
	strict := true
	return &rosetta.ResponseFormat{
		Type:   "json_schema",
		Name:   "govdoc_plan",
		Schema: planSchema,
		Strict: &strict,
	}
}

// planSchema mirrors docmodel.Plan. It is written by hand rather than
// generated from the struct: reflection would turn the doc comments into
// nothing usable, and the important constraints here (the id patterns, the
// 1–4 level range) are not expressible as Go struct tags anyway.
//
// additionalProperties is false everywhere and every property is listed in
// `required`, which is what OpenAI's strict mode demands. Optional fields are
// therefore typed as nullable rather than omitted — a model that has nothing
// to say for `rationale` writes null, which decodes into the same zero value
// as an absent key.
var planSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["sections", "elements", "preamble_node_ids", "edits", "recompose", "meta", "notes"],
  "properties": {
    "meta": {
      "type": "object",
      "additionalProperties": false,
      "required": ["title", "doc_number", "issuing_org", "recipient",
                   "signature", "date", "cc", "printer", "note", "signatory"],
      "properties": {
        "title":       {"type": ["string", "null"]},
        "doc_number":  {"type": ["string", "null"]},
        "issuing_org": {"type": ["string", "null"]},
        "recipient":   {"type": ["string", "null"]},
        "signature":   {"type": ["string", "null"]},
        "date":        {"type": ["string", "null"]},
        "cc":          {"type": ["string", "null"]},
        "printer":     {"type": ["string", "null"]},
        "note":        {"type": ["string", "null"]},
        "signatory":   {"type": ["string", "null"]}
      }
    },
    "elements": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["role", "node_id"],
        "properties": {
          "role": {
            "type": "string",
            "enum": ["title", "recipient", "doc_number", "issuing_org",
                     "signature", "date", "cc", "printer", "note",
                     "attachment_note", "copy_number", "security",
                     "urgency", "signatory"]
          },
          "node_id": {"type": "string", "pattern": "^n[0-9]{4,}$"}
        }
      }
    },
    "preamble_node_ids": {
      "type": "array",
      "items": {"type": "string", "pattern": "^n[0-9]{4,}$"}
    },
    "edits": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["node_id", "kind", "before", "after", "reason", "occurrence"],
        "properties": {
          "node_id": {"type": "string", "pattern": "^n[0-9]{4,}$"},
          "kind": {
            "type": "string",
            "enum": ["typo", "grammar", "wording", "punctuation",
                     "sentence_split", "other"]
          },
          "before":     {"type": "string"},
          "after":      {"type": "string"},
          "reason":     {"type": ["string", "null"]},
          "occurrence": {"type": ["integer", "null"], "minimum": 0}
        }
      }
    },
    "recompose": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["node_id", "parts", "roles", "reason"],
        "properties": {
          "node_id": {"type": "string", "pattern": "^n[0-9]{4,}$"},
          "parts": {"type": "array", "items": {"type": "string"}},
          "roles": {
            "type": "array",
            "items": {"type": ["string", "null"], "enum": ["recipient", "preamble", null]},
            "description": "与 parts 等长；recipient=主送机关，preamble=正文前言，普通正文为 null。二者必须位于开头，且不再放进 body_parts"
          },
          "reason": {"type": ["string", "null"]}
        }
      }
    },
    "sections": {
      "type": "array",
      "items": {"$ref": "#/$defs/section"}
    },
    "notes": {
      "type": "array",
      "items": {"type": "string"}
    }
  },
  "$defs": {
    "section": {
      "type": "object",
      "additionalProperties": false,
      "required": ["id", "title", "level", "heading_node_id",
                   "body_node_ids", "body_parts", "children", "rationale", "preserve_order"],
      "properties": {
        "id":    {"type": "string", "pattern": "^s[0-9a-zA-Z_-]{1,16}$"},
        "title": {"type": "string"},
        "level": {"type": "integer", "minimum": 1, "maximum": 4},
        "heading_node_id": {"type": ["string", "null"]},
        "body_node_ids": {
          "type": "array",
          "items": {"type": "string", "pattern": "^n[0-9]{4,}$"}
        },
        "body_parts": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["node_id", "part"],
            "properties": {
              "node_id": {"type": "string", "pattern": "^n[0-9]{4,}$"},
              "part": {"type": "integer", "minimum": 0}
            }
          }
        },
        "children": {
          "type": "array",
          "items": {"$ref": "#/$defs/section"}
        },
        "rationale":      {"type": ["string", "null"]},
        "preserve_order": {"type": "boolean"}
      }
    }
  }
}`)

// rejectsResponseFormat reports whether err is the endpoint saying it does not
// know response_format.
//
// Two shapes occur in practice:
//
//   - a 400/422 whose message names the field ("unsupported parameter:
//     response_format", "unknown field");
//   - rosetta.ErrNotSupported, which the Anthropic adapter returns for
//     json_object before the request goes out.
//
// The check is deliberately narrow. Treating every 400 as "drop the field"
// would hide real configuration errors behind a second, differently-worded
// failure, so only messages that name the field qualify.
func rejectsResponseFormat(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, rosetta.ErrNotSupported) {
		return true
	}
	var apiErr *rosetta.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != 400 && apiErr.StatusCode != 404 && apiErr.StatusCode != 422 {
		return false
	}
	s := strings.ToLower(apiErr.Message + " " + apiErr.Code + " " + apiErr.Type + " " + apiErr.Param)
	for _, hint := range []string{
		"response_format", "response format", "json_schema", "json schema",
		"output_config", "structured output", "text.format",
	} {
		if strings.Contains(s, hint) {
			return true
		}
	}
	return false
}
