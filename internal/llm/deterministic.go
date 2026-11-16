package llm

import (
	"strings"
	"unicode"

	"govdoc/internal/docmodel"
)

// DeterministicEdits finds the punctuation errors that are decidable from the
// text alone, without asking a model.
//
// # Why the program checks these itself
//
// The review panel has to be trustworthy in two directions. It must not invent
// problems, and it must not silently miss an obvious one because the model's
// attention drifted this run. The second failure was real: the same document
// analysed twice produced different review lists, and once produced none at
// all, even though it contains half-width colons inside Chinese sentences.
//
// A model is the wrong tool for that class of error. "A colon between two Han
// characters in Chinese prose is full-width" is a rule, not a judgement, and a
// rule gives the same answer every time. Running it locally also means the
// panel still has something to review when the model returns no edits — the
// reported "校对项没有生成" case.
//
// # What it deliberately does not check
//
// Only rules with no legitimate exception are included:
//
//   - half-width colon/comma/semicolon between two Han characters;
//   - half-width question/exclamation mark directly after a Han character.
//
// Trailing spaces after the mark are absorbed, because "尺寸: 长" and "尺寸:长"
// are the same error. Parentheses are excluded: "(1)" is a legitimate
// enumeration in many documents, so flagging it would be noise. Anything that
// depends on meaning — a wrong character, a run-on sentence, a semicolon that
// should end a list — is left to the model.
func DeterministicEdits(doc *docmodel.Document) []docmodel.Edit {
	if doc == nil {
		return []docmodel.Edit{}
	}

	out := []docmodel.Edit{}
	for _, n := range doc.ContentNodes() {
		if n.Kind == docmodel.KindTable || n.Kind == docmodel.KindImage {
			continue
		}
		out = append(out, scanNode(n)...)
	}
	return out
}

// scanNode returns the rule-based edits for one node.
func scanNode(n docmodel.Node) []docmodel.Edit {
	runes := []rune(n.Text)
	if len(runes) == 0 {
		return nil
	}

	var out []docmodel.Edit
	for i, r := range runes {
		replacement, ok := replacementFor(r)
		if !ok {
			continue
		}
		if i == 0 || !isHan(runes[i-1]) {
			continue
		}

		// How many runes the source mark occupies, including spaces between
		// the mark and the following character ("尺寸: 长" → ": ").
		span := 1
		next := i + 1
		// Every Unicode space is common after a half-width mark: ASCII space,
		// U+3000, and U+00A0 from Word. Handling only ASCII space made the
		// n0013 "碑座尺寸:<NBSP>上长…" error appear in one run and vanish in
		// the next, which is exactly the run-to-run variance the review list
		// must not have.
		for next < len(runes) && unicode.IsSpace(runes[next]) {
			span++
			next++
		}
		if next >= len(runes) || !isHan(runes[next]) {
			continue
		}

		start := i - editContextRunes
		if start < 0 {
			start = 0
		}
		end := next + editContextRunes
		if end > len(runes) {
			end = len(runes)
		}

		before := string(runes[start:end])
		after := string(runes[start:i]) + replacement + string(runes[next:end])
		occurrence := strings.Count(string(runes[:start]), before)
		out = append(out, docmodel.Edit{
			NodeID:     n.ID,
			Kind:       docmodel.EditPunctuation,
			Before:     before,
			After:      after,
			Reason:     reasonFor(r),
			// The snippet is rebuilt by NormalizeEdits anyway; occurrence is
			// counted there against the final window, so a truncated local
			// snippet cannot shift the target.
			Occurrence: occurrence,
		})
	}

	// A list embedded in one paragraph should be closed with a full stop, not
	// left hanging on a semicolon. This is decidable from the node's end, so
	// the program decides it instead of letting the model add it on some runs.
	trimmed := strings.TrimRightFunc(n.Text, unicode.IsSpace)
	if strings.HasSuffix(trimmed, "；") {
		runes := []rune(trimmed)
		start := len(runes) - 1
		before := "；"
		if len(runes) > editContextRunes+1 {
			start = len(runes) - editContextRunes - 1
			before = string(runes[start:])
		}
		out = append(out, docmodel.Edit{
			NodeID:     n.ID,
			Kind:       docmodel.EditPunctuation,
			Before:     before,
			After:      strings.TrimSuffix(before, "；") + "。",
			Reason:     "列举内容结束后应使用句号，原文以分号收尾",
			Occurrence: strings.Count(string(runes[:start]), before),
		})
	}
	return out
}

// replacementFor maps a half-width mark to its Chinese form. Marks without an
// unambiguous Chinese counterpart are not handled.
func replacementFor(r rune) (string, bool) {
	switch r {
	case ':':
		return "：", true
	case ',':
		return "，", true
	case ';':
		return "；", true
	case '?':
		return "？", true
	case '!':
		return "！", true
	}
	return "", false
}

func reasonFor(r rune) string {
	switch r {
	case ':':
		return "中文行文中应使用全角冒号，原文误用半角冒号"
	case ',':
		return "中文行文中应使用全角逗号，原文误用半角逗号"
	case ';':
		return "中文行文中应使用全角分号，原文误用半角分号"
	case '?':
		return "中文行文中应使用全角问号，原文误用半角问号"
	default:
		return "中文行文中应使用全角感叹号，原文误用半角感叹号"
	}
}

func isHan(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

