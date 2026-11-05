package metadata

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name        string
		html        string
		title       string
		description string
	}{
		{
			name:        "typical page",
			html:        `<!doctype html><html><head><title>Hello</title><meta name="description" content="World"></head><body></body></html>`,
			title:       "Hello",
			description: "World",
		},
		{
			name:        "meta before title",
			html:        `<head><meta name="description" content="D"><title>T</title></head>`,
			title:       "T",
			description: "D",
		},
		{
			name:        "uppercase tags and attributes",
			html:        `<HEAD><TITLE>T</TITLE><META NAME="DESCRIPTION" CONTENT="D"></HEAD>`,
			title:       "T",
			description: "D",
		},
		{
			name:        "self-closing meta with single quotes",
			html:        `<title>T</title><meta content='D' name='description'/>`,
			title:       "T",
			description: "D",
		},
		{
			name:        "whitespace collapsed",
			html:        "<title>\n  A\t\tB \n</title><meta name=description content='  C   D '>",
			title:       "A B",
			description: "C D",
		},
		{
			name:        "entities decoded",
			html:        `<title>Tom &amp; Jerry</title><meta name="description" content="a &lt; b">`,
			title:       "Tom & Jerry",
			description: "a < b",
		},
		{
			name:  "markup inside title is text",
			html:  `<title>a <b>c</b></title>`,
			title: "a <b>c</b>",
		},
		{
			name:  "first title wins",
			html:  `<title>First</title><title>Second</title>`,
			title: "First",
		},
		{
			name:        "first description wins",
			html:        `<title>T</title><meta name="description" content="First"><meta name="description" content="Second">`,
			title:       "T",
			description: "First",
		},
		{
			name:  "other meta tags ignored",
			html:  `<title>T</title><meta property="og:description" content="X"><meta name="keywords" content="Y">`,
			title: "T",
		},
		{
			name: "title in body ignored",
			html: `<head></head><body><title>Not this</title></body>`,
		},
		{
			name: "svg title ignored without a body tag",
			html: `<svg><title>Icon</title></svg>`,
		},
		{
			name:  "empty title",
			html:  `<title></title><p>text</p>`,
			title: "",
		},
		{
			name:  "unclosed title",
			html:  `<title>Truncated`,
			title: "Truncated",
		},
		{
			name: "no metadata",
			html: `<html><head></head></html>`,
		},
		{
			name: "empty document",
			html: ``,
		},
		{
			name: "not really html",
			html: "\x00\x01\x02 garbage",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Extract([]byte(tc.html), "text/html; charset=utf-8")
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != tc.title || got.Description != tc.description {
				t.Errorf("got %+v, want title %q, description %q", got, tc.title, tc.description)
			}
		})
	}
}

func encode1251(t *testing.T, s string) []byte {
	t.Helper()
	encoded, err := charmap.Windows1251.NewEncoder().String(s)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(encoded)
}

func TestExtractDecodesCharsetFromHeader(t *testing.T) {
	body := encode1251(t, `<title>Перевірка</title>`)
	got, err := Extract(body, "text/html; charset=windows-1251")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Перевірка" {
		t.Errorf("title = %q", got.Title)
	}
}

func TestExtractDecodesCharsetFromDocument(t *testing.T) {
	body := encode1251(t, `<meta charset="windows-1251"><title>Перевірка</title>`)
	got, err := Extract(body, "text/html")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Перевірка" {
		t.Errorf("title = %q", got.Title)
	}
}

func TestExtractSanitizesValues(t *testing.T) {
	long := strings.Repeat("я", maxValueBytes)
	got, err := Extract([]byte("<title>"+long+"</title><meta name=description content='a\x00b'>"), "text/html; charset=utf-8")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Title) > maxValueBytes || !utf8.ValidString(got.Title) {
		t.Errorf("title is %d bytes, valid UTF-8: %v", len(got.Title), utf8.ValidString(got.Title))
	}
	if strings.ContainsRune(got.Description, 0) {
		t.Errorf("description contains NUL: %q", got.Description)
	}
}

func TestIsHTML(t *testing.T) {
	cases := []struct {
		contentType string
		body        string
		want        bool
	}{
		{"text/html", "", true},
		{"TEXT/HTML; charset=utf-8", "", true},
		{"application/xhtml+xml", "", true},
		{"application/json", "<html>", false},
		{"application/pdf", "", false},
		{"image/png", "", false},
		{"not a media type;;", "", false},
		{"", "<!doctype html><html></html>", true},
		{"", "plain text", false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := IsHTML(tc.contentType, []byte(tc.body)); got != tc.want {
			t.Errorf("IsHTML(%q, %q) = %v, want %v", tc.contentType, tc.body, got, tc.want)
		}
	}
}
