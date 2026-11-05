// Package metadata extracts SEO metadata from HTML.
package metadata

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// maxValueBytes limits a stored title or description. Search engines show far
// less, and the input comes from an untrusted page.
const maxValueBytes = 4096

// Metadata is what was found in a page. Empty means not found.
type Metadata struct {
	Title       string
	Description string
}

// IsHTML reports whether a response is HTML. A missing Content-Type falls back to
// sniffing the body.
func IsHTML(contentType string, body []byte) bool {
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

// Extract reads the title and meta description from the document head. It decodes
// the charset declared in contentType or the document. The tokenizer tolerates
// malformed and truncated HTML, so only a failing decoder returns an error.
func Extract(body []byte, contentType string) (Metadata, error) {
	if len(body) == 0 {
		return Metadata{}, nil // the charset detector reports an empty input as an error
	}
	reader, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return Metadata{}, err
	}

	var (
		found     Metadata
		titleSeen bool
		descSeen  bool
		tokenizer = html.NewTokenizer(reader)
	)
	for !(titleSeen && descSeen) {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if err := tokenizer.Err(); err != io.EOF {
				return found, err
			}
			return found, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			switch token.Data {
			case "body", "svg":
				// Past the head, a <title> belongs to something else, such as an image.
				return found, nil
			case "title":
				if titleSeen {
					continue
				}
				titleSeen = true
				if tokenizer.Next() == html.TextToken {
					found.Title = clean(tokenizer.Token().Data)
				}
			case "meta":
				if description, ok := metaDescription(token); ok && !descSeen {
					descSeen = true
					found.Description = description
				}
			}
		}
	}
	return found, nil
}

func metaDescription(token html.Token) (string, bool) {
	var name, content string
	for _, attr := range token.Attr {
		switch attr.Key {
		case "name":
			name = attr.Val
		case "content":
			content = attr.Val
		}
	}
	if !strings.EqualFold(strings.TrimSpace(name), "description") {
		return "", false
	}
	return clean(content), true
}

// clean makes page text safe to store: valid UTF-8, no NUL bytes (PostgreSQL TEXT
// rejects them), collapsed whitespace, and a bounded length.
func clean(value string) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.ReplaceAll(value, "\x00", "")
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= maxValueBytes {
		return value
	}
	cut := maxValueBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
