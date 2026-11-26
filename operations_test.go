// Every operation of the document, called through the SDK against the mock: the request it sends (method, path,
// body, headers) and what it makes of the answer; every file method (fixtures["files"]); every error code an
// operation declares, returned as its type; the catalogue's type tree.

package myle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"

	myle "github.com/myleapp/myle-go"
)

var ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()

// target walks from the client to the operation's resource: client.Organization(id).Products.Barcodes.
func target(t *testing.T, c *myle.Client, op fixtureOp) reflect.Value {
	t.Helper()
	var v reflect.Value
	switch op.Call.Scope {
	case "organization":
		v = reflect.ValueOf(c.Organization(op.Call.OrganizationID))
	case "me":
		v = reflect.ValueOf(c.Me)
	default:
		v = reflect.ValueOf(c)
	}
	for _, accessor := range op.Call.Accessors {
		v = v.Elem().FieldByName(goName(accessor))
		if !v.IsValid() {
			t.Fatalf("%s: no resource %s", op.ID, goName(accessor))
		}
	}
	return v
}

// invoke calls a method as a user would: (ctx, ids…, params) with params decoded from the fixture, or nil.
func invoke(t *testing.T, c *myle.Client, op fixtureOp, method string, params json.RawMessage) []reflect.Value {
	t.Helper()
	m := target(t, c, op).MethodByName(goName(method))
	if !m.IsValid() {
		t.Fatalf("%s: no method %s", op.ID, goName(method))
	}
	mt := m.Type()
	if mt.In(0) != ctxType {
		t.Fatalf("%s: the first argument is not a context", op.ID)
	}
	args := []reflect.Value{reflect.ValueOf(context.Background())}
	for _, id := range op.Call.IDs {
		args = append(args, reflect.ValueOf(id))
	}
	if n := len(args); n < mt.NumIn()-1 { // a params pointer before the variadic options
		pt := mt.In(n)
		if pt.Kind() != reflect.Pointer {
			t.Fatalf("%s: argument %d is %s, not params", op.ID, n, pt)
		}
		if len(params) > 0 && string(params) != "null" {
			p := reflect.New(pt.Elem())
			if err := json.Unmarshal(params, p.Interface()); err != nil {
				t.Fatalf("%s: the fixture's params do not fit %s: %v", op.ID, pt, err)
			}
			fillQuery(t, op, p, params)
			args = append(args, p)
		} else {
			args = append(args, reflect.Zero(pt))
		}
	}
	if len(args) != mt.NumIn()-1 {
		t.Fatalf("%s: %s takes %d arguments, the fixture gives %d", op.ID, goName(method), mt.NumIn()-1, len(args))
	}
	return m.Call(args)
}

// fillQuery sets the query parameters of params (the fields tagged json:"-", which json.Unmarshal skips) from the
// fixture's params by name: a file method's params are its query (getInvoicePdf: format).
func fillQuery(t *testing.T, op fixtureOp, p reflect.Value, params json.RawMessage) {
	t.Helper()
	var members map[string]json.RawMessage
	if json.Unmarshal(params, &members) != nil {
		return
	}
	st := p.Elem().Type()
	for name, raw := range members {
		field, ok := st.FieldByName(goName(name))
		if !ok || field.Tag.Get("json") != "-" {
			continue
		}
		if err := json.Unmarshal(raw, p.Elem().FieldByIndex(field.Index).Addr().Interface()); err != nil {
			t.Fatalf("%s: the fixture's %s does not fit %s: %v", op.ID, name, field.Type, err)
		}
	}
}

// result runs the call and answers its value (the first page for a list) and its error.
func result(t *testing.T, c *myle.Client, op fixtureOp) (reflect.Value, error) {
	t.Helper()
	out := invoke(t, c, op, op.Call.Method, op.Call.Params)
	errOf := func(v reflect.Value) error {
		if v.IsNil() {
			return nil
		}
		return v.Interface().(error)
	}
	switch op.Call.Kind {
	case "list":
		page := out[0].MethodByName("FirstPage").Call(nil)
		return page[0], errOf(page[1])
	case "empty":
		return reflect.Value{}, errOf(out[0])
	default:
		return out[0], errOf(out[1])
	}
}

var myleClient = regexp.MustCompile(`^sdk-go/[0-9A-Za-z.+-]+$`)

func TestEveryOperation(t *testing.T) {
	m := newMock(t)
	for _, op := range fixtures.Operations {
		t.Run(op.ID, func(t *testing.T) {
			m.on(nil)
			value, err := result(t, newClient(m, myle.WithMaxRetries(0)), op)
			if err != nil {
				t.Fatalf("%s: %v", op.ID, err)
			}
			got := m.received()
			if len(got) != 1 {
				t.Fatalf("%d requests", len(got))
			}
			r := got[0]
			if r.Method != op.Method || r.Path != concretePath(op) {
				t.Errorf("sent %s %s, want %s %s", r.Method, r.Path, op.Method, concretePath(op))
			}
			if h := r.Header.Get("Authorization"); h != "Bearer test-key" {
				t.Errorf("Authorization %q", h)
			}
			if h := r.Header.Get("Myle-API-Version"); h != fixtures.APIVersion {
				t.Errorf("Myle-API-Version %q", h)
			}
			if h := r.Header.Get("Myle-Client"); !myleClient.MatchString(h) {
				t.Errorf("Myle-Client %q", h)
			}
			if h := r.Header.Get("Accept"); h != "application/json" {
				t.Errorf("Accept %q", h)
			}
			if string(op.Request.Body) != "null" && len(op.Request.Body) > 0 {
				jsonEqual(t, r.Raw, op.Request.Body, "the body")
			} else if len(r.Raw) != 0 {
				t.Errorf("a body was sent: %s", r.Raw)
			}
			if op.Method == "POST" && r.Header.Get("Idempotency-Key") == "" && op.Call.Kind != "list" {
				t.Logf("%s: a POST without an idempotency key", op.ID)
			}
			switch op.Call.Kind {
			case "single":
				jsonEqual(t, mustJSON(value.Interface()), op.Success.Body, "the resource")
				etag := value.MethodByName("ETag")
				if !etag.IsValid() {
					t.Fatalf("the resource has no ETag()")
				}
				if got := etag.Call(nil)[0].String(); got != op.Success.Headers["ETag"] {
					t.Errorf("ETag() %q, want %q", got, op.Success.Headers["ETag"])
				}
				if lr := value.MethodByName("LastResponse").Call(nil)[0].Interface().(*myle.Response); lr.RequestID != fixtures.RequestID {
					t.Errorf("LastResponse().RequestID %q", lr.RequestID)
				}
			case "list":
				var envelope struct {
					Data json.RawMessage `json:"data"`
				}
				_ = json.Unmarshal(op.Success.Body, &envelope)
				jsonEqual(t, mustJSON(value.Elem().FieldByName("Data").Interface()), envelope.Data, "the page")
			}
		})
	}
}

// TestEveryFileMethod calls every file method of fixtures["files"] (DownloadPDF): the request, the bytes untouched,
// the file name and content type, the response; then every error code it declares, returned as its type.
func TestEveryFileMethod(t *testing.T) {
	if len(fixtures.Files) == 0 {
		t.Skip("no operation answers a file")
	}
	m := newMock(t)
	for _, f := range fixtures.Files {
		t.Run(f.ID, func(t *testing.T) {
			op := f.op()
			m.on(nil)
			out := invoke(t, newClient(m, myle.WithMaxRetries(0)), op, f.Call.Method, f.Call.Params)
			if !out[1].IsNil() {
				t.Fatal(out[1].Interface())
			}
			file := out[0].Interface().(*myle.FileDownload)
			got := m.received()
			if len(got) != 1 {
				t.Fatalf("%d requests", len(got))
			}
			r := got[0]
			if r.Method != f.Method || r.Path != concretePath(op) {
				t.Errorf("sent %s %s, want %s %s", r.Method, r.Path, f.Method, concretePath(op))
			}
			if r.Header.Get("Accept") != f.Accept {
				t.Errorf("Accept %q, want %q", r.Header.Get("Accept"), f.Accept)
			}
			query := map[string]string{}
			for name := range r.Query {
				query[name] = r.Query.Get(name)
			}
			if want := f.Request.Query; !reflect.DeepEqual(query, want) && (len(query) > 0 || len(want) > 0) {
				t.Errorf("query %v, want %v", query, want)
			}
			if r.Header.Get("Idempotency-Key") != "" || r.Header.Get("If-Match") != "" {
				t.Errorf("a file download sent Idempotency-Key or If-Match")
			}
			if len(r.Raw) != 0 {
				t.Errorf("a body was sent: %s", r.Raw)
			}
			if !bytes.Equal(file.Data, f.Success.Base64) {
				t.Errorf("Data differs from the fixture's bytes (%d bytes, want %d)", len(file.Data), len(f.Success.Base64))
			}
			if file.ContentType != f.Success.Headers["Content-Type"] {
				t.Errorf("ContentType %q, want %q", file.ContentType, f.Success.Headers["Content-Type"])
			}
			want := ""
			if f.Filename != nil {
				want = *f.Filename
			}
			if file.Filename != want {
				t.Errorf("Filename %q, want %q", file.Filename, want)
			}
			if file.Response == nil || file.Response.StatusCode != f.Success.Status || file.Response.RequestID != fixtures.RequestID {
				t.Errorf("Response %+v", file.Response)
			}

			// Its errors are JSON, returned as their type.
			for _, e := range f.Errors {
				m.on(func(received, int) *reply { return &reply{Status: e.Status, Body: e.Body} })
				out := invoke(t, newClient(m, myle.WithMaxRetries(0)), op, f.Call.Method, f.Call.Params)
				var err error
				if !out[1].IsNil() {
					err = out[1].Interface().(error)
				}
				checkTyped(t, err, e)
			}
		})
	}
}

// asStatus tells whether errors.As matches the type of the status.
func asStatus(err error, status int) bool {
	switch status {
	case 400:
		var e *myle.BadRequestError
		return errors.As(err, &e)
	case 401:
		var e *myle.AuthenticationError
		return errors.As(err, &e)
	case 402:
		var e *myle.PaymentRequiredError
		return errors.As(err, &e)
	case 403:
		var e *myle.PermissionDeniedError
		return errors.As(err, &e)
	case 404:
		var e *myle.NotFoundError
		return errors.As(err, &e)
	case 405:
		var e *myle.MethodNotAllowedError
		return errors.As(err, &e)
	case 406:
		var e *myle.NotAcceptableError
		return errors.As(err, &e)
	case 409:
		var e *myle.ConflictError
		return errors.As(err, &e)
	case 412:
		var e *myle.PreconditionFailedError
		return errors.As(err, &e)
	case 413:
		var e *myle.PayloadTooLargeError
		return errors.As(err, &e)
	case 415:
		var e *myle.UnsupportedMediaTypeError
		return errors.As(err, &e)
	case 422:
		var e *myle.UnprocessableEntityError
		return errors.As(err, &e)
	case 429:
		var e *myle.RateLimitError
		return errors.As(err, &e)
	case 502:
		var e *myle.BadGatewayError
		var i *myle.InternalServerError
		return errors.As(err, &e) && errors.As(err, &i)
	case 503:
		var e *myle.ServiceUnavailableError
		var i *myle.InternalServerError
		return errors.As(err, &e) && errors.As(err, &i)
	}
	var i *myle.InternalServerError
	return status >= 500 && errors.As(err, &i)
}

func checkTyped(t *testing.T, err error, e fixtureError) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s did not fail", e.Code)
	}
	if name := reflect.TypeOf(err).Elem().Name(); name != goName(e.Class) {
		t.Errorf("%s → %s, want %s", e.Code, name, goName(e.Class))
	}
	var me myle.Error
	if !errors.As(err, &me) {
		t.Errorf("%s: not a myle.Error", e.Code)
	}
	var apiErr *myle.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: not an *APIError", e.Code)
	}
	if !asStatus(err, e.Status) {
		t.Errorf("%s: errors.As does not match its status %d", e.Code, e.Status)
	}
	if apiErr.Status != e.Status || apiErr.Code != e.Code || apiErr.Message != e.Body.Error || apiErr.RequestID != e.Body.RequestID {
		t.Errorf("%s: %+v", e.Code, apiErr)
	}
	if context := reflect.ValueOf(err).Elem().FieldByName("Context"); context.IsValid() {
		jsonEqual(t, mustJSON(context.Interface()), e.Body.Context, e.Code+": Context")
	} else {
		jsonEqual(t, apiErr.RawContext, e.Body.Context, e.Code+": RawContext")
	}
}

func TestEveryDeclaredErrorIsItsType(t *testing.T) {
	m := newMock(t)
	for _, op := range fixtures.Operations {
		t.Run(op.ID, func(t *testing.T) {
			for _, e := range op.Errors {
				m.on(func(received, int) *reply { return &reply{Status: e.Status, Body: e.Body} })
				_, err := result(t, newClient(m, myle.WithMaxRetries(0)), op)
				checkTyped(t, err, e)
			}
		})
	}
}

func TestEveryErrorCodeWrapsItsStatusType(t *testing.T) {
	statuses := map[string]int{}
	for _, op := range fixtures.Operations {
		for _, e := range op.Errors {
			statuses[e.Code] = e.Status
		}
	}
	statusTypes := map[int]reflect.Type{
		400: reflect.TypeOf(&myle.BadRequestError{}), 401: reflect.TypeOf(&myle.AuthenticationError{}),
		402: reflect.TypeOf(&myle.PaymentRequiredError{}), 403: reflect.TypeOf(&myle.PermissionDeniedError{}),
		404: reflect.TypeOf(&myle.NotFoundError{}), 405: reflect.TypeOf(&myle.MethodNotAllowedError{}),
		406: reflect.TypeOf(&myle.NotAcceptableError{}), 409: reflect.TypeOf(&myle.ConflictError{}),
		412: reflect.TypeOf(&myle.PreconditionFailedError{}), 413: reflect.TypeOf(&myle.PayloadTooLargeError{}),
		415: reflect.TypeOf(&myle.UnsupportedMediaTypeError{}), 422: reflect.TypeOf(&myle.UnprocessableEntityError{}),
		429: reflect.TypeOf(&myle.RateLimitError{}), 500: reflect.TypeOf(&myle.InternalServerError{}),
		502: reflect.TypeOf(&myle.BadGatewayError{}), 503: reflect.TypeOf(&myle.ServiceUnavailableError{}),
	}
	m := newMock(t)
	for code, class := range fixtures.ErrorClasses {
		status, declared := statuses[code]
		sent := status
		if !declared {
			sent = 418
		}
		m.on(func(received, int) *reply { return &reply{Status: sent, Body: envelope(code, nil)} })
		_, err := newClient(m, myle.WithMaxRetries(0)).Me.Get(context.Background())
		typ := reflect.TypeOf(err)
		if typ == nil || typ.Elem().Name() != goName(class) {
			t.Errorf("%s → %v, want %s", code, typ, goName(class))
			continue
		}
		parent := typ.Elem().Field(0)
		if !parent.Anonymous {
			t.Errorf("%s: %s embeds nothing first", code, typ)
		}
		want := reflect.TypeOf(&myle.APIError{})
		if declared {
			want = statusTypes[status]
		}
		if parent.Type != want {
			t.Errorf("%s: %s wraps %s, want %s", code, typ, parent.Type, want)
		}
		var apiErr *myle.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != code {
			t.Errorf("%s: errors.As *APIError", code)
		}
	}
}
