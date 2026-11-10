package broadcastapp

import (
	"encoding/xml"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ValidateMessageHTML validates a Telegram HTML fragment without modifying it.
// body is administrator input; unsupported tags, entities and nesting return ErrInvalid.
func ValidateMessageHTML(body string) error {
	if !utf8.ValidString(body) || strings.TrimSpace(body) == "" || !validHTMLEntities(body) {
		return ErrInvalid
	}
	// Telegram documents the boolean expandable attribute without an equals sign.
	parsedBody := strings.ReplaceAll(body, "<blockquote expandable>", `<blockquote expandable="">`)
	decoder := xml.NewDecoder(strings.NewReader("<root>" + parsedBody + "</root>"))
	var stack []string
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return ErrInvalid
		}
		switch node := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 && !rootSeen && node.Name.Local == "root" {
				rootSeen = true
				stack = append(stack, "root")
				continue
			}
			if !validHTMLTag(node, stack) {
				return ErrInvalid
			}
			stack = append(stack, node.Name.Local)
		case xml.EndElement:
			if len(stack) == 0 {
				return ErrInvalid
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				return ErrInvalid
			}
		case xml.Comment, xml.Directive, xml.ProcInst:
			return ErrInvalid
		default:
			return ErrInvalid
		}
	}
}

// validHTMLTag checks Telegram's tag/attribute allowlist and entity nesting.
// stack contains open ancestors; false rejects the entire body rather than stripping it.
func validHTMLTag(node xml.StartElement, stack []string) bool {
	name := node.Name.Local
	if node.Name.Space != "" || len(stack) == 0 {
		return false
	}
	for _, parent := range stack {
		if (name == "code" || name == "pre") && parent != "root" && !(name == "code" && parent == "pre") {
			return false
		}
		if parent == "code" || (parent == "pre" && name != "code") || (parent == "blockquote" && name == "blockquote") || (parent == "a" && name == "a") {
			return false
		}
	}
	attrs := make(map[string]string, len(node.Attr))
	for _, attr := range node.Attr {
		if attr.Name.Space != "" {
			return false
		}
		if _, exists := attrs[attr.Name.Local]; exists {
			return false
		}
		attrs[attr.Name.Local] = attr.Value
	}
	switch name {
	case "b", "strong", "i", "em", "u", "ins", "s", "strike", "del", "tg-spoiler", "pre":
		return len(attrs) == 0
	case "span":
		return len(attrs) == 1 && attrs["class"] == "tg-spoiler"
	case "blockquote":
		value, exists := attrs["expandable"]
		return len(attrs) == 0 || (len(attrs) == 1 && exists && value == "")
	case "code":
		class := attrs["class"]
		return len(attrs) == 0 || (len(attrs) == 1 && stack[len(stack)-1] == "pre" && strings.HasPrefix(class, "language-") && len(class) > len("language-"))
	case "tg-emoji":
		id, err := strconv.ParseUint(attrs["emoji-id"], 10, 64)
		return len(attrs) == 1 && err == nil && id > 0
	case "a":
		link, err := url.Parse(attrs["href"])
		return len(attrs) == 1 && err == nil && link.Host != "" && (link.Scheme == "https" || link.Scheme == "http" || link.Scheme == "tg")
	}
	return false
}

// validHTMLEntities rejects raw ampersands and named entities unsupported by Telegram.
// Numeric references are subsequently validated by the strict XML decoder.
func validHTMLEntities(body string) bool {
	for offset := 0; offset < len(body); offset++ {
		if body[offset] != '&' {
			continue
		}
		end := strings.IndexByte(body[offset:], ';')
		if end < 0 {
			return false
		}
		entity := body[offset+1 : offset+end]
		if entity != "lt" && entity != "gt" && entity != "amp" && entity != "quot" && !strings.HasPrefix(entity, "#") {
			return false
		}
		offset += end
	}
	return true
}
