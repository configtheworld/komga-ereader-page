package main

import "testing"

func TestBookID(t *testing.T) {
	for id, want := range map[string]bool{
		"0DJ0GRKDQ5V8Y": true, "a1b2c3d4e5f60718293a4b5c6d7e8f90": true,
		"../etc/passwd": false, "abc": false, "": false, "ab cd efgh": false,
	} {
		if bookIDRe.MatchString(id) != want {
			t.Errorf("id %q want %v", id, want)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		`../../etc/passwd`: "etc passwd",
		`A "Title": x/y`:   "A Title x y",
		"":                 "book",
		"...":              "book",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestContentType(t *testing.T) {
	if contentType(".epub", "") != "application/epub+zip" {
		t.Error("epub")
	}
	if contentType("", "") != "application/octet-stream" {
		t.Error("fallback")
	}
}

func TestContentDisposition(t *testing.T) {
	got := contentDisposition("Käse Buch.epub")
	want := `attachment; filename="K_se Buch.epub"; filename*=UTF-8''K%C3%A4se%20Buch.epub`
	if got != want {
		t.Errorf("got %s", got)
	}
}
