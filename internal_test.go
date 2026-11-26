package myle_test

import (
	"go/format"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	myle "github.com/myleapp/myle-go"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"3", 3 * time.Second, true},
		{" 1.5 ", 1500 * time.Millisecond, true},
		{"-4", 0, true},
		{now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second, true},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{"soon", 0, false},
	}
	for _, c := range cases {
		got, ok := myle.ParseRetryAfter(c.in, now)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: %s %v, want %s %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// The file-name rules of every SDK (filenameOf in TypeScript): filename* first, the last path segment only, none
// when nothing usable is left.
func TestFilenameOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{`inline; filename="F.pdf"; filename*=UTF-8''Facture%20%C3%A9t%C3%A9.pdf`, "Facture été.pdf"},
		{`attachment; filename="../../etc/passwd"`, "passwd"},
		{`attachment; filename=plain.pdf`, "plain.pdf"},
		{`attachment; filename=".."`, ""},
		{`inline`, ""},
		{"", ""},
		{`inline; filename="FA-2026-0042.pdf"`, "FA-2026-0042.pdf"},
		{`attachment; filename="a \"quoted\" name.pdf"`, `a "quoted" name.pdf`},
		{`attachment; filename="C:\\Users\\x\\f.pdf"`, "f.pdf"},
		{`attachment; filename*=UTF-8''..%2F..%2Fsecret.pdf`, "secret.pdf"},
		{`attachment; filename*=iso-8859-1''%E9t%E9.pdf`, "été.pdf"},
		{`attachment; filename="fallback.pdf"; filename*=UTF-8''%FF.pdf`, "fallback.pdf"},
		{`attachment; filename="."`, ""},
		{`attachment; filename=""`, ""},
	}
	for _, c := range cases {
		if got := myle.FilenameOf(c.in); got != c.want {
			t.Errorf("%s: %q, want %q", c.in, got, c.want)
		}
	}
}

// The generator writes gofmt-formatted Go without calling gofmt: check it.
func TestEveryFileIsGofmtClean(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	if len(files) == 0 {
		t.Fatal("no Go file")
	}
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		formatted, err := format.Source(src)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if string(formatted) != string(src) {
			t.Errorf("%s is not gofmt-clean", name)
		}
	}
}
