// A local mock of the Myle API for the tests: 127.0.0.1, a free port, no network. It answers each operation of
// testdata/examples.json (generated from the document by sdk/generator), or what a test's handler says.

package myle_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	myle "github.com/myleapp/myle-go"
)

type fixtureError struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Class  string `json:"class"`
	Body   struct {
		Code      string          `json:"code"`
		Context   json.RawMessage `json:"context"`
		Error     string          `json:"error"`
		RequestID string          `json:"request_id"`
	} `json:"body"`
}

// fixtureFile is a file method (fixtures["files"]): getInvoice as a PDF is getInvoicePdf, InvoicesService.DownloadPDF.
type fixtureFile struct {
	ID          string `json:"id"`
	OperationID string `json:"operationId"`
	Call        struct {
		Scope          string          `json:"scope"`
		OrganizationID string          `json:"organizationId"`
		Accessors      []string        `json:"accessors"`
		Method         string          `json:"method"`
		IDs            []string        `json:"ids"`
		Params         json.RawMessage `json:"params"`
		Kind           string          `json:"kind"`
	} `json:"call"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Accept  string `json:"accept"`
	Request struct {
		PathParams map[string]string `json:"pathParams"`
		Query      map[string]string `json:"query"`
	} `json:"request"`
	Success struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Base64  []byte            `json:"base64"` // encoding/json decodes base64 into a []byte
	} `json:"success"`
	Filename *string        `json:"filename"`
	Errors   []fixtureError `json:"errors"`
}

// op is the file method as a fixtureOp, to reach it with target and invoke.
func (f fixtureFile) op() fixtureOp {
	var op fixtureOp
	op.ID, op.Method, op.Path = f.ID, f.Method, f.Path
	op.Call.Scope, op.Call.OrganizationID, op.Call.Accessors = f.Call.Scope, f.Call.OrganizationID, f.Call.Accessors
	op.Call.Method, op.Call.IDs, op.Call.Params, op.Call.Kind = f.Call.Method, f.Call.IDs, f.Call.Params, f.Call.Kind
	op.Request.PathParams = f.Request.PathParams
	op.Errors = f.Errors
	return op
}

type fixtureOp struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Call   struct {
		Scope          string          `json:"scope"`
		OrganizationID string          `json:"organizationId"`
		Accessors      []string        `json:"accessors"`
		Method         string          `json:"method"`
		IDs            []string        `json:"ids"`
		Params         json.RawMessage `json:"params"`
		Kind           string          `json:"kind"`
	} `json:"call"`
	Request struct {
		PathParams map[string]string `json:"pathParams"`
		Body       json.RawMessage   `json:"body"`
	} `json:"request"`
	Success struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	} `json:"success"`
	Errors []fixtureError `json:"errors"`
}

type fixtureSet struct {
	APIVersion   string            `json:"apiVersion"`
	RequestID    string            `json:"requestId"`
	Operations   []fixtureOp       `json:"operations"`
	Files        []fixtureFile     `json:"files"`
	ErrorClasses map[string]string `json:"errorClasses"`
}

var fixtures = func() fixtureSet {
	data, err := os.ReadFile("testdata/examples.json")
	if err != nil {
		panic(err)
	}
	var f fixtureSet
	if err := json.Unmarshal(data, &f); err != nil {
		panic(err)
	}
	return f
}()

func fixture(t *testing.T, id string) fixtureOp {
	t.Helper()
	for _, op := range fixtures.Operations {
		if op.ID == id {
			return op
		}
	}
	t.Fatalf("no fixture %s", id)
	return fixtureOp{}
}

func fileFixture(t *testing.T, id string) fixtureFile {
	t.Helper()
	for _, f := range fixtures.Files {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no file fixture %s", id)
	return fixtureFile{}
}

var pathParam = regexp.MustCompile(`\{(\w+)\}`)

// concretePath is the path of a fixture operation, with its example ids.
func concretePath(op fixtureOp) string {
	return pathParam.ReplaceAllStringFunc(op.Path, func(m string) string { return op.Request.PathParams[m[1:len(m)-1]] })
}

// received is a request the mock got.
type received struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Raw    []byte
	Body   any
}

// reply is what the mock answers; Drop closes the connection without an answer, Truncate sends half a body.
type reply struct {
	Status   int
	Header   map[string]string
	Body     any
	Raw      string
	Bytes    []byte
	Drop     bool
	Truncate bool
	Delay    time.Duration
}

type handler func(r received, attempt int) *reply

type mockAPI struct {
	server *httptest.Server
	URL    string

	mu      sync.Mutex
	got     []received
	handler handler
}

func newMock(t *testing.T) *mockAPI {
	t.Helper()
	m := &mockAPI{}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	m.URL = m.server.URL
	t.Cleanup(m.server.Close)
	return m
}

// on answers with h first; what it leaves (nil) is answered from the fixtures.
func (m *mockAPI) on(h handler) *mockAPI {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handler = h
	m.got = nil
	return m
}

func (m *mockAPI) received() []received {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]received(nil), m.got...)
}

func (m *mockAPI) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	rec := received{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Raw: raw}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &rec.Body)
	}
	m.mu.Lock()
	m.got = append(m.got, rec)
	attempt := 0
	for _, g := range m.got {
		if g.Method == rec.Method && g.Path == rec.Path {
			attempt++
		}
	}
	h := m.handler
	m.mu.Unlock()

	var answer *reply
	if h != nil {
		answer = h(rec, attempt)
	}
	if answer == nil {
		answer = fixtureReply(rec)
	}
	if answer == nil {
		answer = &reply{Status: 404, Body: map[string]any{
			"code": "core.not-found", "context": map[string]any{"kind": "path", "name": r.URL.Path}, "error": "Not found", "request_id": "x",
		}}
	}
	if answer.Delay > 0 {
		select {
		case <-time.After(answer.Delay):
		case <-r.Context().Done():
			return
		}
	}
	if answer.Drop || answer.Truncate {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		if answer.Truncate {
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"id\":")
			_ = buf.Flush()
		}
		_ = conn.Close()
		return
	}
	w.Header().Set("Myle-Request-Id", fixtures.RequestID)
	for k, v := range answer.Header {
		w.Header().Set(k, v)
	}
	var body []byte
	switch {
	case answer.Bytes != nil:
		body = answer.Bytes
	case answer.Raw != "":
		body = []byte(answer.Raw)
	case answer.Body != nil:
		if rawJSON, ok := answer.Body.(json.RawMessage); ok {
			body = rawJSON
		} else {
			body, _ = json.Marshal(answer.Body)
		}
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(answer.Status)
	_, _ = w.Write(body)
}

func fixtureReply(r received) *reply {
	for _, f := range fixtures.Files {
		if f.Method == r.Method && concretePath(f.op()) == r.Path && f.Accept == r.Header.Get("Accept") {
			return &reply{Status: f.Success.Status, Header: f.Success.Headers, Bytes: f.Success.Base64}
		}
	}
	for _, op := range fixtures.Operations {
		if op.Method != r.Method || concretePath(op) != r.Path {
			continue
		}
		rep := &reply{Status: op.Success.Status, Header: op.Success.Headers}
		if len(op.Success.Body) > 0 {
			rep.Body = op.Success.Body
		}
		return rep
	}
	return nil
}

// newClient is a client of the mock, silent on deprecations.
func newClient(m *mockAPI, opts ...myle.ClientOption) *myle.Client {
	base := []myle.ClientOption{myle.WithAPIKey("test-key"), myle.WithBaseURL(m.URL), myle.WithDeprecationHandler(nil)}
	return myle.NewClient(append(base, opts...)...)
}

// envelope is an error answer's body.
func envelope(code string, context map[string]any) map[string]any {
	if context == nil {
		context = map[string]any{}
	}
	return map[string]any{"code": code, "context": context, "error": code + "!", "request_id": "REQ"}
}

// initialisms are the words Go spells in capitals, as the generator does (emit_go.INITIALISMS).
var initialisms = map[string]string{
	"api": "API", "csv": "CSV", "html": "HTML", "http": "HTTP", "id": "ID", "ids": "IDs", "ip": "IP", "json": "JSON",
	"pdf": "PDF", "sql": "SQL", "uri": "URI", "url": "URL", "uuid": "UUID", "xml": "XML",
}

// goName is the Go name of a document's name: getNeighbors → GetNeighbors, apiKeys → APIKeys, downloadPdf → DownloadPDF.
func goName(name string) string {
	var b strings.Builder
	for _, chunk := range regexp.MustCompile(`[^A-Za-z0-9]+`).Split(name, -1) {
		for _, w := range splitCamel(chunk) {
			w = strings.ToLower(w)
			if up, ok := initialisms[w]; ok {
				b.WriteString(up)
			} else if w != "" {
				b.WriteString(strings.ToUpper(w[:1]) + w[1:])
			}
		}
	}
	return b.String()
}

// splitCamel splits like naming.words: `listMyApiKeys` → list, My, Api, Keys; `APIKey` → API, Key.
func splitCamel(s string) []string {
	var words []string
	runes := []rune(s)
	start := 0
	for i := 1; i <= len(runes); i++ {
		if i == len(runes) {
			words = append(words, string(runes[start:i]))
			break
		}
		prev, cur := runes[i-1], runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		lowerToUpper := (unicode.IsLower(prev) || unicode.IsDigit(prev)) && unicode.IsUpper(cur)
		acronymEnd := unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(next)
		if lowerToUpper || acronymEnd {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	return words
}

// jsonEqual compares two JSON documents as values.
func jsonEqual(t *testing.T, got, want []byte, what string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: not JSON: %v: %s", what, err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: the fixture is not JSON: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got  %s\n want %s", what, got, want)
	}
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprint(err))
	}
	return bytes.TrimSpace(data)
}
