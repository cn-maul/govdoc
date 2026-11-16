package llm

import (
	"encoding/json"
	"strconv"
	"strings"

	"govdoc/internal/docmodel"
)

// ParsePartialPlan reads the complete objects out of a plan that is still
// being generated.
//
// The model writes one JSON object, so the answer cannot be decoded as a whole
// until the final brace arrives. The useful parts of that object, however,
// are independent arrays: an edit is complete as soon as its closing brace
// appears, and a top-level section is complete as soon as its own object
// closes. This function extracts those complete elements and ignores the
// unfinished tail. It is deliberately forgiving: a partial answer is a
// progress signal, not a contract, and the final answer still goes through
// ParsePlan and VerifyPlan.
func ParsePartialPlan(text string, doc *docmodel.Document) (*docmodel.Plan, []docmodel.Edit) {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return nil, nil
	}
	body := text[start:]

	plan := &docmodel.Plan{
		Sections:  []docmodel.Section{},
		Edits:     []docmodel.Edit{},
		Recompose: []docmodel.Recompose{},
	}

	if arr := findTopLevelArray(body, "edits"); arr >= 0 {
		for _, raw := range completeObjects(body[arr:]) {
			var edit docmodel.Edit
			if err := json.Unmarshal([]byte(raw), &edit); err != nil {
				continue
			}
			if edit.NodeID == "" || edit.Before == "" {
				continue
			}
			plan.Edits = append(plan.Edits, edit)
		}
	}

	if arr := findTopLevelArray(body, "sections"); arr >= 0 {
		plan.Sections = completeSections(body[arr:])
	}

	// Recompositions are standalone top-level objects, so each one becomes
	// visible as soon as its closing brace arrives. A half-written parts array
	// is ignored, exactly like a half-written edit.
	if arr := findTopLevelArray(body, "recompose"); arr >= 0 {
		for _, raw := range completeObjects(body[arr:]) {
			var rec docmodel.Recompose
			if err := json.Unmarshal([]byte(raw), &rec); err != nil {
				continue
			}
			if rec.NodeID == "" || len(rec.Parts) == 0 {
				continue
			}
			plan.Recompose = append(plan.Recompose, rec)
		}
	}

	if len(plan.Sections) == 0 && len(plan.Edits) == 0 && len(plan.Recompose) == 0 {
		return nil, nil
	}
	return plan, plan.Edits
}

// completeSections reconstructs the section tree from a still-open array.
//
// completeObjects alone is too coarse here: a document whose whole outline is
// nested under one top-level section would show nothing until that section's
// final brace arrived, which is most of the answer. Instead this keeps a stack
// of open objects. A closed object is decoded whole (it contains every
// completed descendant), and an object that is still open contributes the
// fields it already has plus the children that have closed so far.
func completeSections(s string) []docmodel.Section {
	type frame struct {
		array        bool
		sectionArray bool
		section      bool
		start        int
		children     []docmodel.Section
	}

	var roots []docmodel.Section
	// s starts immediately after the top-level "sections" bracket, so the
	// implicit root container is already a section array.
	stack := []frame{{array: true, sectionArray: true}}
	inString := false
	escaped := false
	// pendingKey is the last object key outside a string; it tells the first
	// "[" whether that array holds sections.
	pendingKey := ""

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			name, next, ok := readJSONString(s, i)
			if !ok {
				return roots
			}
			j := skipSpace(s, next)
			if j < len(s) && s[j] == ':' {
				pendingKey = name
			}
			// Walk the closing quote normally so escapes are handled once.
			i = next - 1
		case '{':
			parent := stack[len(stack)-1]
			stack = append(stack, frame{
				start:   i,
				section: parent.array && parent.sectionArray && pendingKey == "",
			})
			pendingKey = ""
		case '[':
			stack = append(stack, frame{
				array:        true,
				sectionArray: pendingKey == "sections" || pendingKey == "children",
			})
			pendingKey = ""
		case '}':
			if len(stack) == 0 {
				continue
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if f.array {
				continue
			}
			if !f.section {
				continue
			}

			var section docmodel.Section
			if err := json.Unmarshal([]byte(s[f.start:i+1]), &section); err != nil {
				// A balanced object can still fail strict decoding only if it
				// is malformed; keep whatever fields the tolerant scan read.
				section = partialSectionFields(s[f.start : i+1])
				section.Children = f.children
			}
			if !usefulSection(section) {
				continue
			}
			owner := -1
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].section {
					owner = k
					break
				}
			}
			if owner < 0 {
				roots = append(roots, section)
			} else {
				stack[owner].children = append(stack[owner].children, section)
			}
			pendingKey = ""
		case ']':
			if len(stack) > 1 {
				f := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if !f.array {
					// A "]" closes an array, so the matching "[" frame must be
					// on top. Keep the stack usable even for a malformed tail.
					stack = append(stack, f)
				}
			}
			pendingKey = ""
		case ',':
			pendingKey = ""
		}
	}

	// Section objects still on the stack are mid-write. Their completed
	// children are already in f.children; nested open sections are added
	// from the inside out so a finished grandchild can be shown before its
	// parent's closing brace arrives.
	var open []int
	for i := range stack {
		if stack[i].section {
			open = append(open, i)
		}
	}
	for i := len(open) - 1; i >= 0; i-- {
		idx := open[i]
		section := partialSectionFields(s[stack[idx].start:])
		section.Children = append(section.Children, stack[idx].children...)
		if !usefulSection(section) {
			continue
		}
		if i > 0 {
			parent := open[i-1]
			stack[parent].children = append(stack[parent].children, section)
		} else {
			roots = append(roots, section)
		}
	}
	return roots
}

func usefulSection(s docmodel.Section) bool {
	// An id alone is not something the outline can render. Keep a section
	// once its title is complete, or when it already has visible children to
	// hoist. This is what lets a half-written parent carry completed
	// descendants without adding an empty row to the structure panel.
	return s.Title != "" || len(s.Children) > 0
}

// partialSectionFields reads the fields that have already arrived in one
// section object. Keys nested inside children are ignored, so a child's title
// cannot replace its parent's.
func partialSectionFields(raw string) docmodel.Section {
	var section docmodel.Section
	depth := 0
	inString := false
	escaped := false

	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			name, next, ok := readJSONString(raw, i)
			if !ok {
				return section
			}
			if depth != 1 {
				i = next - 1
				continue
			}
			j := skipSpace(raw, next)
			if j >= len(raw) || raw[j] != ':' {
				i = next - 1
				continue
			}
			value := skipSpace(raw, j+1)
			switch name {
			case "id":
				section.ID, _ = jsonStringAt(raw, value)
			case "title":
				section.Title, _ = jsonStringAt(raw, value)
			case "heading_node_id":
				section.HeadingNodeID, _ = jsonStringAt(raw, value)
			case "level":
				section.Level = jsonIntAt(raw, value)
			}
			// Continue scanning from the opening quote of the value rather
			// than past it, so its string/container is tracked normally.
			i = next - 1
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return section
}

func jsonStringAt(s string, start int) (string, bool) {
	if start >= len(s) || s[start] != '"' {
		return "", false
	}
	_, next, ok := readJSONString(s, start)
	if !ok {
		return "", false
	}
	var out string
	if err := json.Unmarshal([]byte(s[start:next]), &out); err != nil {
		return "", false
	}
	return out, true
}

func jsonIntAt(s string, start int) int {
	end := start
	for end < len(s) {
		if (s[end] >= '0' && s[end] <= '9') || s[end] == '-' {
			end++
			continue
		}
		break
	}
	n, err := strconv.Atoi(s[start:end])
	if err != nil {
		return 0
	}
	return n
}

// findTopLevelArray returns the index immediately after the opening bracket
// of a named array in a JSON object, or -1 when the array has not started.
//
// Names are matched only outside strings. An outline title can contain the
// word "sections"; scanning naively for the key would then start parsing in
// the middle of a title.
func findTopLevelArray(s, key string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '"' {
			continue
		}
		name, next, ok := readJSONString(s, i)
		if !ok {
			return -1
		}
		i = next - 1
		if name != key {
			continue
		}

		j := skipSpace(s, next)
		if j >= len(s) || s[j] != ':' {
			continue
		}
		j = skipSpace(s, j+1)
		if j >= len(s) || s[j] != '[' {
			return -1
		}
		return j + 1
	}
	return -1
}

// completeObjects returns the complete brace-balanced objects in an array
// fragment. The final, still-open object is omitted.
func completeObjects(s string) []string {
	var out []string
	depth := 0
	start := -1
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, s[start:i+1])
				start = -1
			}
		}
	}
	return out
}

// readJSONString reads a JSON string starting at the opening quote and returns
// its contents plus the index just past the closing quote.
func readJSONString(s string, start int) (string, int, bool) {
	if start >= len(s) || s[start] != '"' {
		return "", start, false
	}
	escaped := false
	for i := start + 1; i < len(s); i++ {
		switch {
		case escaped:
			escaped = false
		case s[i] == '\\':
			escaped = true
		case s[i] == '"':
			return s[start+1 : i], i + 1, true
		}
	}
	return "", len(s), false
}

func skipSpace(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}
