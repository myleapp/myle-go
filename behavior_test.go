// The client's behavior: pagination, retries, idempotency keys, Retry-After, If-Match, merge patches, deprecation
// notices, headers, credentials, timeouts, connections, tolerance, file downloads. Against the local mock, no network.

package myle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	myle "github.com/myleapp/myle-go"
)

const (
	org     = "organization_0193a7c2-5b1e-7c3d-9f40-1a2b3c4d5e6f"
	product = "product_0193a7c2-5b1e-7c3d-9f40-1a2b3c4d5e6f"
)

var (
	productPath = "/organizations/" + org + "/products/" + product
	listPath    = "/organizations/" + org + "/products"
	ctx         = context.Background()
)

func productBody(t *testing.T, n int) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(fixture(t, "getProduct").Success.Body, &p); err != nil {
		t.Fatal(err)
	}
	p["id"] = "product_" + string(rune('0'+n))
	p["name"] = "P" + string(rune('0'+n))
	return p
}

func paged(t *testing.T, m *mockAPI) {
	pages := map[string]map[string]any{
		"":   {"data": []any{productBody(t, 1), productBody(t, 2)}, "nextCursor": "c2"},
		"c2": {"data": []any{productBody(t, 3)}, "nextCursor": "c3"},
		"c3": {"data": []any{productBody(t, 4)}},
	}
	m.on(func(r received, _ int) *reply {
		if r.Path != listPath {
			return nil
		}
		return &reply{Status: 200, Body: pages[r.Query.Get("cursor")]}
	})
}

func cursors(m *mockAPI) []string {
	var out []string
	for _, r := range m.received() {
		out = append(out, r.Query.Get("cursor"))
	}
	return out
}

func TestPaginationFollowsNextCursorWithTheFilters(t *testing.T) {
	m := newMock(t)
	paged(t, m)
	var names []string
	params := &myle.ProductListParams{Status: []myle.ProductStatus{myle.ProductStatusActive}, Tag: []string{"bio", "local"}}
	for p, err := range newClient(m).Organization(org).Products.List(ctx, params).All() {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "P1,P2,P3,P4" {
		t.Errorf("names %v", names)
	}
	if strings.Join(cursors(m), ",") != ",c2,c3" {
		t.Errorf("cursors %v", cursors(m))
	}
	for _, r := range m.received() {
		if strings.Join(r.Query["status"], ",") != "ACTIVE" || strings.Join(r.Query["tag"], ",") != "bio,local" {
			t.Errorf("filters %v", r.Query)
		}
	}
}

func TestFirstPageNextPageCollect(t *testing.T) {
	m := newMock(t)
	paged(t, m)
	list := newClient(m).Organization(org).Products.List(ctx, &myle.ProductListParams{Limit: myle.Int(2)})
	first, err := list.FirstPage()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Data) != 2 || !first.HasMore() || first.NextCursor != "c2" || first.LastResponse().StatusCode != 200 {
		t.Errorf("first page %+v", first)
	}
	if m.received()[0].Query.Get("limit") != "2" {
		t.Errorf("limit not sent")
	}
	second, _ := first.NextPage()
	third, _ := second.NextPage()
	if len(second.Data) != 1 || third.HasMore() {
		t.Errorf("second %v third %v", second.Data, third.HasMore())
	}
	if after, err := third.NextPage(); after != nil || err != nil {
		t.Errorf("after the last page: %v %v", after, err)
	}
	if got, _ := list.CollectMax(3); len(got) != 3 {
		t.Errorf("CollectMax(3): %d", len(got))
	}
	if got, _ := list.Collect(); len(got) != 4 {
		t.Errorf("Collect: %d", len(got))
	}
	pages := 0
	for page, err := range list.Pages() {
		if err != nil {
			t.Fatal(err)
		}
		pages += len(page.Data)
	}
	if pages != 4 {
		t.Errorf("Pages: %d items", pages)
	}
}

func TestAGivenCursorStartsThereAndCollectMaxFetchesNoMore(t *testing.T) {
	m := newMock(t)
	paged(t, m)
	got, err := newClient(m).Organization(org).Products.List(ctx, &myle.ProductListParams{Cursor: myle.String("c2")}).Collect()
	if err != nil || len(got) != 2 || got[0].Name != "P3" {
		t.Errorf("from c2: %v %v", got, err)
	}
	paged(t, m)
	products := newClient(m).Organization(org).Products
	if got, _ := products.List(ctx, nil).CollectMax(2); len(got) != 2 || len(m.received()) != 1 {
		t.Errorf("CollectMax(2): %d items, %d requests", len(got), len(m.received()))
	}
	if got, _ := products.List(ctx, nil).CollectMax(0); len(got) != 0 || len(m.received()) != 1 {
		t.Errorf("CollectMax(0) sent a request")
	}
	for range products.List(ctx, nil).All() {
		break
	}
	if len(m.received()) != 2 {
		t.Errorf("breaking out fetched more: %d", len(m.received()))
	}
}

func TestAListWithoutCursorIsOnePage(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply {
		return &reply{Status: 200, Body: map[string]any{"data": []string{"a", "b"}, "nextCursor": "ignored"}}
	})
	tags, err := newClient(m).Organization(org).Products.ListTags(ctx).Collect()
	if err != nil || strings.Join(tags, ",") != "a,b" || len(m.received()) != 1 {
		t.Errorf("tags %v %v, %d requests", tags, err, len(m.received()))
	}
}

func TestPaginationErrorEndsTheIteration(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply { return &reply{Status: 404, Body: envelope("core.not-found", nil)} })
	n := 0
	for _, err := range newClient(m).Organization(org).Products.List(ctx, nil).All() {
		n++
		var nf *myle.CoreNotFoundError
		if !errors.As(err, &nf) {
			t.Errorf("err %v", err)
		}
	}
	if n != 1 {
		t.Errorf("%d yields", n)
	}
}

var createParams = &myle.ProductCreateParams{Number: "A1", Name: "Foin", VatRate: "5.5"}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func keys(m *mockAPI) []string {
	var out []string
	for _, r := range m.received() {
		out = append(out, r.Header.Get("Idempotency-Key"))
	}
	return out
}

func TestOneIdempotencyKeyPerCallTheSameOnRetries(t *testing.T) {
	m := newMock(t)
	m.on(func(r received, attempt int) *reply {
		if r.Method == "POST" && attempt == 1 {
			return &reply{Status: 503, Body: envelope("core.unavailable", nil)}
		}
		return nil
	})
	created, err := newClient(m, myle.WithMaxRetries(2)).Organization(org).Products.Create(ctx, createParams)
	if err != nil || created.Object != "product" {
		t.Fatalf("%v %v", created, err)
	}
	k := keys(m)
	if len(k) != 2 || !uuidPattern.MatchString(k[0]) || k[0] != k[1] {
		t.Errorf("keys %v", k)
	}
	m.on(nil)
	_, _ = newClient(m).Organization(org).Products.Create(ctx, createParams)
	if keys(m)[0] == k[0] {
		t.Errorf("a new call reused the key")
	}
}

func TestADroppedConnectionIsRetriedWithTheCallersKey(t *testing.T) {
	m := newMock(t)
	m.on(func(r received, attempt int) *reply {
		if r.Method == "POST" && attempt == 1 {
			return &reply{Drop: true}
		}
		return nil
	})
	_, err := newClient(m, myle.WithMaxRetries(1)).Organization(org).Products.Create(ctx, createParams, myle.IdempotencyKey("order-2026-0042"))
	if err != nil || strings.Join(keys(m), ",") != "order-2026-0042,order-2026-0042" {
		t.Errorf("%v %v", keys(m), err)
	}
}

func TestAReplayIsTold(t *testing.T) {
	m := newMock(t)
	m.on(func(r received, _ int) *reply {
		if r.Method == "POST" {
			return &reply{Status: 201, Header: map[string]string{"Myle-Idempotent-Replayed": "true", "ETag": `"7"`}, Body: productBody(t, 9)}
		}
		return nil
	})
	created, err := newClient(m).Organization(org).Products.Create(ctx, createParams)
	if err != nil || !created.LastResponse().IdempotentReplayed || created.ETag() != `"7"` {
		t.Errorf("%v %v", created, err)
	}
}

func TestAWriteWithoutKeyIsNotRetriedOn5xx(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply { return &reply{Status: 500, Body: envelope("core.internal-error", nil)} })
	_, err := newClient(m, myle.WithMaxRetries(2)).Organization(org).Products.Update(ctx, product, &myle.ProductUpdateParams{Name: myle.String("x")})
	var ise *myle.InternalServerError
	if !errors.As(err, &ise) || len(m.received()) != 1 {
		t.Errorf("%v, %d requests", err, len(m.received()))
	}
}

func TestAGetIsRetriedOn5xx(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply {
		return &reply{Status: 502, Body: envelope("core.bad-gateway", map[string]any{"service": "einvoicing"})}
	})
	_, err := newClient(m, myle.WithMaxRetries(1)).Organization(org).Products.Get(ctx, product)
	var bg *myle.CoreBadGatewayError
	if !errors.As(err, &bg) || bg.Context.Service != "einvoicing" || len(m.received()) != 2 {
		t.Errorf("%v, %d requests", err, len(m.received()))
	}
}

func TestIdempotencyKeyReusedIsNotRetried(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply {
		return &reply{Status: 422, Body: envelope("core.idempotency-key-reused", nil)}
	})
	_, err := newClient(m).Organization(org).Products.Create(ctx, createParams)
	var reused *myle.CoreIdempotencyKeyReusedError
	if !errors.As(err, &reused) || len(m.received()) != 1 {
		t.Errorf("%v, %d requests", err, len(m.received()))
	}
}

func TestIdempotencyKeyInUseIsRetriedAfterRetryAfter(t *testing.T) {
	m := newMock(t)
	m.on(func(_ received, attempt int) *reply {
		if attempt == 1 {
			return &reply{Status: 409, Header: map[string]string{"Retry-After": "0"}, Body: envelope("core.idempotency-key-in-use", nil)}
		}
		return nil
	})
	if _, err := newClient(m).Organization(org).Products.Create(ctx, createParams); err != nil {
		t.Fatal(err)
	}
	if k := keys(m); len(k) != 2 || k[0] != k[1] {
		t.Errorf("keys %v", k)
	}
}

func TestRateLimitWaitsRetryAfterSecondsEvenForAWrite(t *testing.T) {
	m := newMock(t)
	m.on(func(_ received, attempt int) *reply {
		if attempt == 1 {
			return &reply{Status: 429, Header: map[string]string{"Retry-After": "1"}, Body: envelope("core.rate-limited", map[string]any{"policy": "write"})}
		}
		return nil
	})
	started := time.Now()
	updated, err := newClient(m).Organization(org).Products.Update(ctx, product, &myle.ProductUpdateParams{Name: myle.String("x")})
	if err != nil || updated.Object != "product" {
		t.Fatal(err)
	}
	if waited := time.Since(started); waited < 950*time.Millisecond {
		t.Errorf("waited %s", waited)
	}
	if len(m.received()) != 2 {
		t.Errorf("%d requests", len(m.received()))
	}
}

func TestRateLimitRetryAfterAsAnHTTPDate(t *testing.T) {
	m := newMock(t)
	m.on(func(_ received, attempt int) *reply {
		if attempt == 1 {
			date := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
			return &reply{Status: 429, Header: map[string]string{"Retry-After": date}, Body: envelope("core.rate-limited", map[string]any{"policy": "read"})}
		}
		return nil
	})
	started := time.Now()
	if _, err := newClient(m).Organization(org).Products.Get(ctx, product); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(started); waited < 900*time.Millisecond || waited > 3*time.Second {
		t.Errorf("waited %s", waited)
	}
}

func TestWhenRetriesRunOutTheErrorCarriesRetryAfter(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply {
		return &reply{Status: 429, Header: map[string]string{"Retry-After": "0"}, Body: envelope("core.rate-limited", map[string]any{"policy": "read"})}
	})
	_, err := newClient(m, myle.WithMaxRetries(2)).Organization(org).Products.Get(ctx, product)
	var limited *myle.CoreRateLimitedError
	var rate *myle.RateLimitError
	if !errors.As(err, &limited) || !errors.As(err, &rate) || limited.Context.Policy != "read" {
		t.Fatalf("%v", err)
	}
	if limited.RetryAfter == nil || *limited.RetryAfter != 0 || len(m.received()) != 3 {
		t.Errorf("RetryAfter %v, %d requests", limited.RetryAfter, len(m.received()))
	}
}

func TestALongRetryAfterIsNotWaitedFor(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply {
		return &reply{Status: 429, Header: map[string]string{"Retry-After": "3600"}, Body: envelope("core.rate-limited", map[string]any{"policy": "read"})}
	})
	_, err := newClient(m).Organization(org).Products.Get(ctx, product)
	var limited *myle.CoreRateLimitedError
	if !errors.As(err, &limited) || *limited.RetryAfter != time.Hour || len(m.received()) != 1 {
		t.Errorf("%v, %d requests", err, len(m.received()))
	}
}

func conditional(t *testing.T, m *mockAPI) {
	current := `"2"`
	m.on(func(r received, _ int) *reply {
		if r.Path != productPath {
			return nil
		}
		if r.Method == "GET" {
			return &reply{Status: 200, Header: map[string]string{"ETag": current}, Body: productBody(t, 1)}
		}
		if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && ifMatch != current {
			return &reply{Status: 412, Body: envelope("core.precondition-failed", nil)}
		}
		if r.Method == "DELETE" {
			return &reply{Status: 204}
		}
		return &reply{Status: 200, Header: map[string]string{"ETag": `"3"`}, Body: productBody(t, 1)}
	})
}

func TestEverySingleResourceCarriesItsETag(t *testing.T) {
	m := newMock(t)
	conditional(t, m)
	p, err := newClient(m).Organization(org).Products.Get(ctx, product)
	if err != nil || p.ETag() != `"2"` || p.LastResponse().StatusCode != 200 || p.LastResponse().RequestID != fixtures.RequestID {
		t.Fatalf("%v %v", p, err)
	}
	if bytes.Contains(mustJSON(p), []byte("ETag")) {
		t.Errorf("the JSON of a resource holds its meta")
	}
}

func TestIfMatchTakesTheResourceOrItsETag(t *testing.T) {
	m := newMock(t)
	conditional(t, m)
	products := newClient(m).Organization(org).Products
	p, _ := products.Get(ctx, product)
	updated, err := products.Update(ctx, product, &myle.ProductUpdateParams{Name: myle.String("x")}, myle.IfMatch(p))
	if err != nil || updated.ETag() != `"3"` {
		t.Fatalf("%v %v", updated, err)
	}
	if r := m.received()[1]; r.Header.Get("If-Match") != `"2"` || r.Header.Get("Content-Type") != "application/merge-patch+json" {
		t.Errorf("headers %v", r.Header)
	}
	_, err = products.Update(ctx, product, &myle.ProductUpdateParams{Name: myle.String("y")}, myle.IfMatch(myle.ETag(`"1"`)))
	var stale *myle.CorePreconditionFailedError
	if !errors.As(err, &stale) || stale.Status != 412 {
		t.Errorf("stale update: %v", err)
	}
	if err := products.Delete(ctx, product, myle.IfMatch(myle.ETag(`"1"`))); !errors.As(err, &stale) {
		t.Errorf("stale delete: %v", err)
	}
	if err := products.Delete(ctx, product, myle.IfMatch(updated)); err == nil {
		t.Errorf("a delete with the newer etag should be stale for this mock")
	}
	if err := products.Delete(ctx, product, myle.IfMatch(p)); err != nil {
		t.Errorf("delete: %v", err)
	}
}

func TestOptionsAnOperationDoesNotTakeFailBeforeSending(t *testing.T) {
	m := newMock(t)
	m.on(nil)
	products := newClient(m).Organization(org).Products
	if _, err := products.Get(ctx, product, myle.IfMatch(myle.ETag(`"1"`))); err == nil || !strings.Contains(err.Error(), "If-Match") {
		t.Errorf("IfMatch on a GET: %v", err)
	}
	if _, err := products.Get(ctx, product, myle.IdempotencyKey("k")); err == nil {
		t.Errorf("IdempotencyKey on a GET: no error")
	}
	if len(m.received()) != 0 {
		t.Errorf("sent %d requests", len(m.received()))
	}
}

func TestAMergePatchSendsNullAndLeavesOutWhatIsNotGiven(t *testing.T) {
	m := newMock(t)
	conditional(t, m)
	_, err := newClient(m).Organization(org).Products.Update(ctx, product, &myle.ProductUpdateParams{
		AccountingNumber: myle.Null[string](),
		Name:             myle.String("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, m.received()[0].Raw, []byte(`{"accountingNumber":null,"name":"x"}`), "the patch")
	_, _ = newClient(m).Organization(org).Products.Update(ctx, product, nil)
	jsonEqual(t, m.received()[1].Raw, []byte(`{}`), "a nil patch")
}

func TestNestedRequestObjects(t *testing.T) {
	m := newMock(t)
	m.on(nil)
	_, err := newClient(m).Organization(org).Products.Create(ctx, &myle.ProductCreateParams{
		Number: "A1", Name: "Foin", VatRate: "5.5",
		UnitPrice:    myle.Value(myle.Money{Amount: "12.50", Currency: "EUR"}),
		InitialStock: myle.Value(myle.InitialStock{Quantity: myle.String("3"), Lot: myle.Value(myle.InitialStockLot{Number: "L1"})}),
		Tags:         myle.Value([]string{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, m.received()[0].Raw, []byte(`{"number":"A1","name":"Foin","unitPrice":{"amount":"12.50","currency":"EUR"},"vatRate":"5.5","tags":[],"initialStock":{"quantity":"3","lot":{"number":"L1"}}}`), "the body")
}

func TestHeaders(t *testing.T) {
	m := newMock(t)
	m.on(nil)
	if _, err := newClient(m, myle.WithAPIVersion("2027-03-01"), myle.WithAcceptLanguage("fr"), myle.WithHeader("X-Default", "1")).Me.Get(ctx); err != nil {
		t.Fatal(err)
	}
	h := m.received()[0].Header
	if h.Get("Myle-API-Version") != "2027-03-01" || h.Get("Accept-Language") != "fr" || h.Get("Authorization") != "Bearer test-key" ||
		!myleClient.MatchString(h.Get("Myle-Client")) || h.Get("Myle-Client") != "sdk-go/"+myle.Version || h.Get("X-Default") != "1" {
		t.Errorf("headers %v", h)
	}
	if _, err := newClient(m).Me.Get(ctx, myle.AcceptLanguage("en"), myle.Header("Traceparent", "00-abc-def-01")); err != nil {
		t.Fatal(err)
	}
	h = m.received()[1].Header
	if h.Get("Accept-Language") != "en" || h.Get("Traceparent") != "00-abc-def-01" || h.Get("Myle-API-Version") != myle.APIVersion {
		t.Errorf("headers %v", h)
	}
	if _, err := newClient(m, myle.WithMyleClient("acme/1.2.0 (42)")).Me.Get(ctx); err != nil || m.received()[2].Header.Get("Myle-Client") != "acme/1.2.0 (42)" {
		t.Errorf("Myle-Client replaced: %v", err)
	}
	if newClient(m).APIVersion() != myle.APIVersion || myle.APIVersion != fixtures.APIVersion {
		t.Errorf("APIVersion %s", myle.APIVersion)
	}
}

func TestAMalformedAPIVersionIsRefusedBeforeSending(t *testing.T) {
	m := newMock(t)
	m.on(nil)
	if _, err := newClient(m, myle.WithAPIVersion("latest")).Me.Get(ctx); err == nil {
		t.Errorf("no error")
	}
	if len(m.received()) != 0 {
		t.Errorf("sent")
	}
}

func deprecated(t *testing.T, m *mockAPI) {
	m.on(func(r received, _ int) *reply {
		var body any = productBody(t, 1)
		if strings.HasSuffix(r.Path, "/me") {
			body = fixture(t, "getMe").Success.Body
		}
		return &reply{Status: 200, Header: map[string]string{
			"Deprecation": "@1772323200",
			"Sunset":      "Tue, 01 Sep 2027 00:00:00 GMT",
			"Link":        `<https://developers.myleapp.fr/changelog/2027-03-01>; rel="deprecation"`,
		}, Body: body}
	})
}

func TestDeprecationIsNoticedOncePerOperationAndClient(t *testing.T) {
	m := newMock(t)
	deprecated(t, m)
	var notices []myle.DeprecationNotice
	c := newClient(m, myle.WithDeprecationHandler(func(n myle.DeprecationNotice) { notices = append(notices, n) }))
	_, _ = c.Me.Get(ctx)
	_, _ = c.Me.Get(ctx)
	_, _ = c.Organization(org).Products.Get(ctx, product)
	if len(notices) != 2 {
		t.Fatalf("%d notices", len(notices))
	}
	want := myle.DeprecationNotice{
		OperationID: "getMe", Method: "GET", Path: "/me", APIVersion: fixtures.APIVersion,
		Deprecation: "@1772323200", Sunset: "Tue, 01 Sep 2027 00:00:00 GMT", Link: "https://developers.myleapp.fr/changelog/2027-03-01",
	}
	if notices[0] != want {
		t.Errorf("notice %+v", notices[0])
	}
	other := newClient(m, myle.WithDeprecationHandler(func(n myle.DeprecationNotice) { notices = append(notices, n) }))
	_, _ = other.Me.Get(ctx)
	if len(notices) != 3 {
		t.Errorf("another client is noticed again: %d", len(notices))
	}
}

func TestTheDefaultNoticeIsALogLine(t *testing.T) {
	m := newMock(t)
	deprecated(t, m)
	var out bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(saved)
	c := myle.NewClient(myle.WithAPIKey("k"), myle.WithBaseURL(m.URL))
	_, _ = c.Me.Get(ctx)
	_, _ = c.Me.Get(ctx)
	if n := strings.Count(out.String(), "deprecated"); n != 1 || !strings.Contains(out.String(), "/changelog/2027-03-01") {
		t.Errorf("log %q", out.String())
	}
}

func TestTheKeyComesFromTheEnvironmentAndPublicOperationsNeedNone(t *testing.T) {
	m := newMock(t)
	m.on(nil)
	t.Setenv("MYLE_API_KEY", "from-env")
	t.Setenv("MYLE_BASE_URL", m.URL)
	if _, err := myle.NewClient().Me.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if h := m.received()[0].Header.Get("Authorization"); h != "Bearer from-env" {
		t.Errorf("Authorization %q", h)
	}
	t.Setenv("MYLE_API_KEY", "")
	anonymous := myle.NewClient()
	if _, err := anonymous.Me.Get(ctx); !errors.Is(err, myle.ErrMissingAPIKey) {
		t.Errorf("without a key: %v", err)
	}
	if len(m.received()) != 1 {
		t.Errorf("a call without a key was sent")
	}
	rights, err := anonymous.Rights.List(ctx).FirstPage()
	if err != nil || len(rights.Data) == 0 {
		t.Fatalf("rights: %v", err)
	}
	if h := m.received()[1].Header.Get("Authorization"); h != "" {
		t.Errorf("a public call sent Authorization %q", h)
	}
}

func TestUnknownCodesFallBackOnTheirStatusType(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply {
		return &reply{Status: 401, Body: envelope("auth.credential.something-new", nil)}
	})
	_, err := newClient(m).Me.Get(ctx)
	var auth *myle.AuthenticationError
	if !errors.As(err, &auth) || auth.Code != "auth.credential.something-new" {
		t.Errorf("%T %v", err, err)
	}
	if _, ok := err.(*myle.AuthenticationError); !ok {
		t.Errorf("%T is not exactly *AuthenticationError", err)
	}
	m.on(func(received, int) *reply { return &reply{Status: 418, Raw: "I'm a teapot"} })
	_, err = newClient(m).Me.Get(ctx)
	var apiErr *myle.APIError
	if _, ok := err.(*myle.APIError); !ok || !errors.As(err, &apiErr) || apiErr.Message != "I'm a teapot" {
		t.Errorf("%T %v", err, err)
	}
	m.on(func(received, int) *reply {
		return &reply{Status: 404, Body: envelope("core.not-found", map[string]any{"kind": "product", "name": product})}
	})
	_, err = newClient(m).Organization(org).Products.Get(ctx, product)
	var nf *myle.CoreNotFoundError
	if !errors.As(err, &nf) || nf.Context.Kind != "product" || !strings.Contains(err.Error(), "core.not-found") {
		t.Errorf("%v", err)
	}
}

func TestUnknownMembersAndEnumValuesAreTolerated(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply {
		p := productBody(t, 1)
		p["status"] = "ARCHIVED_SOMEDAY"
		p["aMemberFromTheFuture"] = map[string]any{"x": 1}
		return &reply{Status: 200, Body: p}
	})
	p, err := newClient(m).Organization(org).Products.Get(ctx, product)
	if err != nil || p.Status != myle.ProductStatus("ARCHIVED_SOMEDAY") || !bytes.Contains(p.LastResponse().Body, []byte("aMemberFromTheFuture")) {
		t.Errorf("%v %v", p, err)
	}
}

func TestATimeoutIsATimeoutErrorAfterTheRetries(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply { return &reply{Status: 200, Delay: time.Second, Body: map[string]any{}} })
	_, err := newClient(m, myle.WithMaxRetries(1), myle.WithTimeout(50*time.Millisecond)).Me.Get(ctx)
	var timeout *myle.TimeoutError
	var conn *myle.ConnectionError
	if !errors.As(err, &timeout) || !errors.As(err, &conn) || len(m.received()) != 2 {
		t.Errorf("%T %v, %d requests", err, err, len(m.received()))
	}
	_, err = newClient(m, myle.WithMaxRetries(0)).Me.Get(ctx, myle.Timeout(50*time.Millisecond))
	if !errors.As(err, &timeout) {
		t.Errorf("a call's timeout: %v", err)
	}
}

func TestTimeoutZeroMeansNone(t *testing.T) {
	m := newMock(t)
	m.on(nil)
	if _, err := newClient(m, myle.WithTimeout(0)).Me.Get(ctx); err != nil {
		t.Error(err)
	}
}

func TestABodyCutShortIsAConnectionErrorRetriedWhenSafe(t *testing.T) {
	m := newMock(t)
	m.on(func(_ received, attempt int) *reply {
		if attempt == 1 {
			return &reply{Truncate: true}
		}
		return nil
	})
	if _, err := newClient(m, myle.WithMaxRetries(1)).Me.Get(ctx); err != nil || len(m.received()) != 2 {
		t.Errorf("%v, %d requests", err, len(m.received()))
	}
	m.on(func(received, int) *reply { return &reply{Truncate: true} })
	_, err := newClient(m, myle.WithMaxRetries(0)).Me.Get(ctx)
	var conn *myle.ConnectionError
	var timeout *myle.TimeoutError
	if !errors.As(err, &conn) || errors.As(err, &timeout) {
		t.Errorf("%T %v", err, err)
	}
}

func TestARefusedConnection(t *testing.T) {
	_, err := myle.NewClient(myle.WithAPIKey("k"), myle.WithBaseURL("http://127.0.0.1:9"), myle.WithMaxRetries(0)).Me.Get(ctx)
	var conn *myle.ConnectionError
	var timeout *myle.TimeoutError
	var me myle.Error
	if !errors.As(err, &conn) || errors.As(err, &timeout) || !errors.As(err, &me) {
		t.Errorf("%T %v", err, err)
	}
}

func TestACancelledContextIsReturnedNotRetried(t *testing.T) {
	m := newMock(t)
	m.on(nil)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := newClient(m).Me.Get(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	if len(m.received()) != 0 {
		t.Errorf("sent")
	}
	m.on(func(received, int) *reply { return &reply{Status: 200, Delay: time.Second, Body: map[string]any{}} })
	short, cancel2 := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel2()
	if _, err := newClient(m, myle.WithMaxRetries(3)).Me.Get(short); !errors.Is(err, context.DeadlineExceeded) || len(m.received()) != 1 {
		t.Errorf("%v, %d requests", err, len(m.received()))
	}
}

type countingTransport struct{ n atomic.Int32 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

func TestWithHTTPClient(t *testing.T) {
	m := newMock(t)
	m.on(nil)
	transport := &countingTransport{}
	if _, err := newClient(m, myle.WithHTTPClient(&http.Client{Transport: transport})).Me.Get(ctx); err != nil || transport.n.Load() != 1 {
		t.Errorf("%v, %d round trips", err, transport.n.Load())
	}
}

func TestOptJSON(t *testing.T) {
	var absent myle.Opt[string]
	if absent.IsSet() || absent.IsNull() {
		t.Errorf("zero Opt is set")
	}
	if v, ok := myle.String("x").Get(); !ok || v != "x" {
		t.Errorf("Get")
	}
	var o struct {
		A myle.Opt[int] `json:"a"`
		B myle.Opt[int] `json:"b"`
		C myle.Opt[int] `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":null,"b":3}`), &o); err != nil {
		t.Fatal(err)
	}
	if !o.A.IsNull() || !o.B.IsSet() || o.C.IsSet() {
		t.Errorf("%+v", o)
	}
	if string(mustJSON(myle.Int(3))) != "3" || string(mustJSON(myle.Null[int]())) != "null" {
		t.Errorf("MarshalJSON")
	}
}

// Files: DownloadPDF (getInvoicePdf), the file method of getInvoice.

const invoice = "invoice_0193a7c2-5b1e-7c3d-9f40-1a2b3c4d5e6f"

var invoicePath = "/organizations/" + org + "/invoices/" + invoice

func TestDownloadPDFSendsAcceptAndTheFormatAndKeepsTheBytes(t *testing.T) {
	f := fileFixture(t, "getInvoicePdf")
	pdf := f.Success.Base64
	if !bytes.Contains(pdf, []byte{0xe2, 0xe3, 0xcf, 0xd3}) {
		t.Fatal("the fixture PDF lost its non-UTF-8 bytes")
	}
	m := newMock(t)
	file, err := newClient(m).Organization(org).Invoices.DownloadPDF(ctx, invoice, &myle.GetInvoicePDFParams{
		Format: myle.Value(myle.GetInvoicePDFFormatFacturx),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := m.received()[0]
	if r.Method != "GET" || r.Path != invoicePath || r.Header.Get("Accept") != "application/pdf" {
		t.Errorf("sent %s %s Accept %q", r.Method, r.Path, r.Header.Get("Accept"))
	}
	if r.Query.Encode() != "format=facturx" {
		t.Errorf("query %q", r.Query.Encode())
	}
	if !bytes.Equal(file.Data, pdf) {
		t.Errorf("the bytes changed")
	}
	if file.ContentType != "application/pdf" || file.Filename != "invoice-0001.pdf" {
		t.Errorf("ContentType %q Filename %q", file.ContentType, file.Filename)
	}
	if file.Response.StatusCode != 200 || file.Response.RequestID != fixtures.RequestID {
		t.Errorf("Response %+v", file.Response)
	}
	// nil params: no query.
	if _, err := newClient(m).Organization(org).Invoices.DownloadPDF(ctx, invoice, nil); err != nil {
		t.Fatal(err)
	}
	if q := m.received()[1].Query; len(q) != 0 {
		t.Errorf("nil params sent %v", q)
	}
}

func TestTheJSONGetStillAsksForJSONWithoutFormat(t *testing.T) {
	m := newMock(t)
	invoiceValue, err := newClient(m).Organization(org).Invoices.Get(ctx, invoice)
	if err != nil || invoiceValue.ID == "" {
		t.Fatalf("%v %v", invoiceValue, err)
	}
	r := m.received()[0]
	if r.Header.Get("Accept") != "application/json" || r.Query.Has("format") {
		t.Errorf("Accept %q, query %v", r.Header.Get("Accept"), r.Query)
	}
}

func TestTheFileNameComesFromContentDisposition(t *testing.T) {
	cases := []struct {
		disposition string // "-": no header
		want        string
	}{
		{`inline; filename="F.pdf"; filename*=UTF-8''Facture%20%C3%A9t%C3%A9.pdf`, "Facture été.pdf"},
		{`attachment; filename="../../etc/passwd"`, "passwd"},
		{`attachment; filename=plain.pdf`, "plain.pdf"},
		{`attachment; filename=".."`, ""},
		{`inline`, ""},
		{"-", ""},
	}
	m := newMock(t)
	for _, c := range cases {
		m.on(func(received, int) *reply {
			header := map[string]string{"Content-Type": "application/pdf"}
			if c.disposition != "-" {
				header["Content-Disposition"] = c.disposition
			}
			return &reply{Status: 200, Header: header, Bytes: []byte("%PDF-1.4")}
		})
		file, err := newClient(m).Organization(org).Invoices.DownloadPDF(ctx, invoice, nil)
		if err != nil {
			t.Fatal(err)
		}
		if file.Filename != c.want {
			t.Errorf("%s: Filename %q, want %q", c.disposition, file.Filename, c.want)
		}
	}
}

func TestADownloadIsRetriedAfterA429(t *testing.T) {
	m := newMock(t)
	m.on(func(_ received, attempt int) *reply {
		if attempt == 1 {
			return &reply{Status: 429, Header: map[string]string{"Retry-After": "0"}, Body: envelope("core.rate-limited", nil)}
		}
		return nil
	})
	file, err := newClient(m).Organization(org).Invoices.DownloadPDF(ctx, invoice, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(m.received()); n != 2 || !bytes.Equal(file.Data, fileFixture(t, "getInvoicePdf").Success.Base64) {
		t.Errorf("%d requests, %d bytes", n, len(file.Data))
	}
}

func TestADownloadErrorIsItsType(t *testing.T) {
	m := newMock(t)
	m.on(func(received, int) *reply {
		return &reply{Status: 409, Body: envelope("billing.invoice.facturx-unavailable", nil)}
	})
	_, err := newClient(m).Organization(org).Invoices.DownloadPDF(ctx, invoice, nil)
	var unavailable *myle.BillingInvoiceFacturxUnavailableError
	var conflict *myle.ConflictError
	if !errors.As(err, &unavailable) || !errors.As(err, &conflict) {
		t.Errorf("%T %v", err, err)
	}
}

func TestSaveWritesTheExactBytes(t *testing.T) {
	pdf := fileFixture(t, "getInvoicePdf").Success.Base64
	file := &myle.FileDownload{Data: pdf, Filename: "invoice-0001.pdf"}
	path := filepath.Join(t.TempDir(), file.Filename)
	if err := file.Save(path); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(written, pdf) {
		t.Errorf("written %d bytes, want %d (%v)", len(written), len(pdf), err)
	}
}
