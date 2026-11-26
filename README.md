# myle-go

The official Go SDK of the [Myle API](https://developers.myleapp.fr): typed resources, one error type per
error code (`errors.As`), auto-pagination with Go 1.23 iterators, idempotent retries, `ETag` / `If-Match`.
Standard library only, Go ≥ 1.23. Generated from the API's OpenAPI document by `sdk/generator` (Apache-2.0).

SDK version `0.1.1`, built against `Myle-API-Version: 2026-11-21`.

```sh
go get github.com/myleapp/myle-go
```

```go
import "github.com/myleapp/myle-go"

client := myle.NewClient() // reads MYLE_API_KEY; or myle.NewClient(myle.WithAPIKey("…"))
me, err := client.Me.Get(ctx)
org := client.Organization("organization_…")
```

## Five common tasks

### List, page after page

```go
params := &myle.ProductListParams{Status: []myle.ProductStatus{myle.ProductStatusActive}, Limit: myle.Int(200)}
for product, err := range org.Products.List(ctx, params).All() { // follows nextCursor
	if err != nil {
		return err
	}
	fmt.Println(product.Number, product.Name)
}

page, err := org.Products.List(ctx, nil).FirstPage() // one page: page.Data, page.HasMore(), page.NextPage()
firstThousand, err := org.Companies.List(ctx, &myle.CompanyListParams{Q: myle.String("gaec")}).CollectMax(1000)
```

### Create

```go
product, err := org.Products.Create(ctx, &myle.ProductCreateParams{
	Number:  "A0012",
	Name:    "Foin de prairie",
	VatRate: "5.5",
	Tags:    myle.Value([]string{"fourrage"}),
})
```

Every create sends an `Idempotency-Key`, generated once and reused by its retries: a create retried after a
network error never creates twice. `myle.IdempotencyKey("order-2026-0042")` chooses it;
`product.LastResponse().IdempotentReplayed` tells a replay.

### Update only the version you read (`If-Match`)

```go
product, err := org.Products.Get(ctx, "product_…")
updated, err := org.Products.Update(ctx, product.ID, &myle.ProductUpdateParams{
	Name:             myle.String("Foin de luzerne"), // sent
	AccountingNumber: myle.Null[string](),            // sent as null: cleared
	// a member left out is unchanged
}, myle.IfMatch(product)) // or myle.IfMatch(myle.ETag(`"3"`))
var stale *myle.CorePreconditionFailedError
if errors.As(err, &stale) {
	// someone changed it since: read it again
}
fmt.Println(updated.ETag())
```

### Handle errors

```go
_, err := org.Products.Barcodes.Create(ctx, "product_…", &myle.ProductBarcodeCreateParams{
	Value:  "3017620422003",
	Format: myle.BarcodeFormatEan13,
})
var duplicate *myle.ProductBarcodeDuplicateError
var conflict *myle.ConflictError
var apiErr *myle.APIError
var connErr *myle.ConnectionError
switch {
case errors.As(err, &duplicate): // the code's type, with its typed Context
	fmt.Println("already there:", duplicate.Context.Value)
case errors.As(err, &conflict): // its status's type
	fmt.Println(conflict.Code)
case errors.As(err, &apiErr): // any error answer
	fmt.Println(apiErr.Status, apiErr.Code, apiErr.Message, apiErr.RequestID)
case errors.As(err, &connErr): // no answer (*myle.TimeoutError for a timeout)
	fmt.Println(connErr)
}
```

### Download an invoice's PDF

```go
file, err := org.Invoices.DownloadPDF(ctx, "invoice_…", &myle.GetInvoicePDFParams{
	Format: myle.Value(myle.GetInvoicePDFFormatFacturx), // the Factur-X; the plain PDF by default
})
if err != nil {
	return err
}
err = file.Save(cmp.Or(file.Filename, "invoice.pdf"))
```

A method answering a file returns a `*myle.FileDownload`: `Data` (the bytes as received, never decoded),
`Filename` (from `Content-Disposition`: `filename*=UTF-8''…` first, else `filename`, cut to its last path
segment so it never names another directory; `""` when the API sent none), `ContentType` and `Response`
(status, headers, `RequestID`); `Save(path)` writes the bytes. It sends `Accept: application/pdf`, never an
idempotency key nor `If-Match`; retries and typed errors are those of any call. The JSON `Get` keeps
`Accept: application/json`.

## The client

```go
client := myle.NewClient(
	myle.WithAPIKey("…"),                     // default: MYLE_API_KEY (an organization or a user key, opaque)
	myle.WithAPIVersion("2026-11-21"),          // the Myle-API-Version sent; default: myle.APIVersion
	myle.WithBaseURL("https://api.myleapp.fr"), // default: MYLE_BASE_URL, else this
	myle.WithMaxRetries(2),                     // network errors, 429 and 5xx (when safe to repeat)
	myle.WithTimeout(60*time.Second),           // per attempt, the body included; 0 for none
	myle.WithAcceptLanguage("fr"),              // the language of error messages; English by default
	myle.WithHTTPClient(httpClient),            // a proxy, a custom transport
	myle.WithDeprecationHandler(func(n myle.DeprecationNotice) { log.Print(n) }), // nil: silent
)
```

Resources follow the API's paths: `client.Organization(id).Products` is `/organizations/{id}/products`,
`client.Me` is `/me`. Every method takes a `context.Context`, the ids of its path, then its params (a
pointer to its `XxxParams` struct, `nil` for none: the body's members and the list's filters together),
then request options: `myle.IfMatch`, `myle.IdempotencyKey` where the operation takes them,
`myle.AcceptLanguage`, `myle.Header`, `myle.MaxRetries`, `myle.Timeout`. The context bounds the whole
call, retries included; its cancellation is returned as it is (`context.Canceled`).

**Optional and null.** A member of a request that may be left out is a `myle.Opt[T]`: its zero value is
absent (not sent), `myle.String("x")` / `myle.Int(3)` / `myle.Value(v)` send a value, `myle.Null[T]()`
sends `null`, which clears the member in a merge patch (`Update`). Required members are plain fields. In
responses, a member that may be `null` is a pointer (`*string`, `*myle.Money`), a list is a slice.

**Enums are open**: `myle.ProductStatus` is a `string` type with known constants
(`myle.ProductStatusActive`); a value this SDK does not know yet arrives as it is. Unknown members of an
answer are ignored by the structs and kept in `LastResponse().Body`.

## Errors

Every error answer of the API is returned as the type of its error code, which wraps the type of its HTTP
status (`*myle.ConflictError`, `*myle.NotFoundError`…), which wraps `*myle.APIError`: `errors.As` matches
at each level. `Code` is stable, `Context` is typed by the code, `Message` is the human sentence (French or
English: never parse it), `RequestID` is what support asks for, `RetryAfter` is set when the API sent one.
An unknown code arrives as its status's type. A network failure is a `*myle.ConnectionError`
(`*myle.TimeoutError` for a timeout); a call needing a key without one returns `myle.ErrMissingAPIKey`
before sending.

| Code | Status | Type |
|---|---|---|
| `core.bad-request` | 400 | `*myle.CoreBadRequestError` |
| `core.api-version-unsupported` | — | `*myle.CoreAPIVersionUnsupportedError` |
| `core.module-required` | 402 | `*myle.CoreModuleRequiredError` |
| `core.subscription-required` | 402 | `*myle.CoreSubscriptionRequiredError` |
| `core.plan-limit-reached` | 402 | `*myle.CorePlanLimitReachedError` |
| `core.not-found` | 404 | `*myle.CoreNotFoundError` |
| `core.method-not-allowed` | 405 | `*myle.CoreMethodNotAllowedError` |
| `core.not-acceptable` | 406 | `*myle.CoreNotAcceptableError` |
| `core.conflict` | 409 | `*myle.CoreConflictError` |
| `core.precondition-failed` | 412 | `*myle.CorePreconditionFailedError` |
| `core.payload-too-large` | 413 | `*myle.CorePayloadTooLargeError` |
| `core.unsupported-media-type` | 415 | `*myle.CoreUnsupportedMediaTypeError` |
| `core.validation-failed` | 422 | `*myle.CoreValidationFailedError` |
| `core.idempotency-key-reused` | 422 | `*myle.CoreIdempotencyKeyReusedError` |
| `core.idempotency-key-in-use` | 409 | `*myle.CoreIdempotencyKeyInUseError` |
| `core.rate-limited` | 429 | `*myle.CoreRateLimitedError` |
| `core.internal-error` | 500 | `*myle.CoreInternalError` |
| `core.bad-gateway` | 502 | `*myle.CoreBadGatewayError` |
| `core.unavailable` | 503 | `*myle.CoreUnavailableError` |
| `auth.credential.missing` | 401 | `*myle.AuthCredentialMissingError` |
| `auth.credential.invalid` | 401 | `*myle.AuthCredentialInvalidError` |
| `auth.credential.expired` | 401 | `*myle.AuthCredentialExpiredError` |
| `auth.credential.revoked` | 401 | `*myle.AuthCredentialRevokedError` |
| `auth.right.missing` | 403 | `*myle.AuthRightMissingError` |
| `auth.email.unconfirmed` | 403 | `*myle.AuthEmailUnconfirmedError` |
| `auth.session.refused` | 403 | `*myle.AuthSessionRefusedError` |
| `auth.api-key.organization-not-allowed` | 403 | `*myle.AuthAPIKeyOrganizationNotAllowedError` |
| `auth.session.required` | 403 | `*myle.AuthSessionRequiredError` |
| `auth.step-up.required` | 403 | `*myle.AuthStepUpRequiredError` |
| `auth.api-key.limit-reached` | 409 | `*myle.AuthAPIKeyLimitReachedError` |
| `products.barcode.duplicate` | 409 | `*myle.ProductBarcodeDuplicateError` |
| `products.barcode.outside-company-prefix` | 422 | `*myle.ProductBarcodeOutsideCompanyPrefixError` |
| `crm.company.identifier-required` | 422 | `*myle.CrmCompanyIdentifierRequiredError` |
| `crm.company.invalid-identifier` | 422 | `*myle.CrmCompanyInvalidIdentifierError` |
| `crm.company.invalid-siret` | 422 | `*myle.CrmCompanyInvalidSiretError` |
| `crm.company.accounting-number-taken` | 409 | `*myle.CrmCompanyAccountingNumberTakenError` |
| `crm.postal-address.referenced` | 409 | `*myle.CrmPostalAddressReferencedError` |
| `billing.invoice.not-a-draft` | 409 | `*myle.BillingInvoiceNotADraftError` |
| `billing.invoice.stripe-imported` | 409 | `*myle.BillingInvoiceStripeImportedError` |
| `billing.invoice.foreign-product` | 422 | `*myle.BillingInvoiceForeignProductError` |
| `billing.invoice.out-of-scope-reference` | 422 | `*myle.BillingInvoiceOutOfScopeReferenceError` |
| `billing.invoice.partial-credit-note` | 409 | `*myle.BillingInvoicePartialCreditNoteError` |
| `billing.invoice.facturx-unavailable` | 409 | `*myle.BillingInvoiceFacturxUnavailableError` |
| `billing.invoice.not-payable` | 409 | `*myle.BillingInvoiceNotPayableError` |
| `billing.invoice.already-paid` | 409 | `*myle.BillingInvoiceAlreadyPaidError` |
| `billing.invoice.credit-note-linked` | 409 | `*myle.BillingInvoiceCreditNoteLinkedError` |
| `billing.payment.not-removable` | 409 | `*myle.BillingPaymentNotRemovableError` |
| `billing.payment.stripe-collected` | 409 | `*myle.BillingPaymentStripeCollectedError` |
| `billing.invoice.not-creditable` | 409 | `*myle.BillingInvoiceNotCreditableError` |
| `billing.invoice.already-credited` | 409 | `*myle.BillingInvoiceAlreadyCreditedError` |
| `billing.invoice.deposit-deducted` | 409 | `*myle.BillingInvoiceDepositDeductedError` |
| `billing.invoice.einvoice-required` | 409 | `*myle.BillingInvoiceEinvoiceRequiredError` |
| `billing.invoice.recipient-unsubscribed` | — | `*myle.BillingInvoiceRecipientUnsubscribedError` |
| `billing.invoice.transition-refused` | 409 | `*myle.BillingInvoiceTransitionRefusedError` |
| `billing.invoice.generated-credit-note` | 409 | `*myle.BillingInvoiceGeneratedCreditNoteError` |
| `billing.invoice.not-editable` | 409 | `*myle.BillingInvoiceNotEditableError` |
| `billing.invoice.not-reinvoiceable` | 409 | `*myle.BillingInvoiceNotReinvoiceableError` |
| `billing.invoice.already-reinvoiced` | 409 | `*myle.BillingInvoiceAlreadyReinvoicedError` |
| `billing.invoice.not-a-credit-note` | 409 | `*myle.BillingInvoiceNotACreditNoteError` |
| `billing.invoice.correction-fixed` | 409 | `*myle.BillingInvoiceCorrectionFixedError` |
| `billing.invoice.not-correctable` | 409 | `*myle.BillingInvoiceNotCorrectableError` |
| `billing.invoice.not-duplicable` | — | `*myle.BillingInvoiceNotDuplicableError` |
| `quotations.quotation.transition-refused` | 409 | `*myle.QuotationQuotationTransitionRefusedError` |
| `quotations.quotation.not-extendable` | 409 | `*myle.QuotationQuotationNotExtendableError` |
| `quotations.quotation.converted` | 409 | `*myle.QuotationQuotationConvertedError` |
| `quotations.quotation.already-invoiced` | 409 | `*myle.QuotationQuotationAlreadyInvoicedError` |
| `quotations.quotation.has-deposits` | 409 | `*myle.QuotationQuotationHasDepositsError` |
| `quotations.quotation.not-editable` | 409 | `*myle.QuotationQuotationNotEditableError` |
| `quotations.quotation.foreign-product` | 422 | `*myle.QuotationQuotationForeignProductError` |
| `quotations.quotation.out-of-scope-reference` | 422 | `*myle.QuotationQuotationOutOfScopeReferenceError` |
| `quotations.quotation.recipient-unsubscribed` | — | `*myle.QuotationQuotationRecipientUnsubscribedError` |
| `orders.order.transition-refused` | 409 | `*myle.OrderOrderTransitionRefusedError` |
| `orders.order.not-deletable` | 409 | `*myle.OrderOrderNotDeletableError` |
| `orders.order.not-editable` | 409 | `*myle.OrderOrderNotEditableError` |
| `orders.order.foreign-product` | 422 | `*myle.OrderOrderForeignProductError` |
| `orders.order.out-of-scope-reference` | 422 | `*myle.OrderOrderOutOfScopeReferenceError` |
| `orders.order.already-invoiced` | 409 | `*myle.OrderOrderAlreadyInvoicedError` |
| `orders.order.has-deposits` | 409 | `*myle.OrderOrderHasDepositsError` |
| `orders.order.shop-billed` | 409 | `*myle.OrderOrderShopBilledError` |
| `orders.order.recipient-unsubscribed` | — | `*myle.OrderOrderRecipientUnsubscribedError` |
| `einvoicing.organization.paid-plan-required` | 402 | `*myle.EinvoicingOrganizationPaidPlanRequiredError` |
| `einvoicing.organization.not-subject` | 409 | `*myle.EinvoicingOrganizationNotSubjectError` |
| `einvoicing.connection.not-connected` | 409 | `*myle.EinvoicingConnectionNotConnectedError` |
| `einvoicing.routing-address.not-published` | 422 | `*myle.EinvoicingRoutingAddressNotPublishedError` |
| `einvoicing.einvoice.not-eligible` | 409 | `*myle.EinvoicingEinvoiceNotEligibleError` |
| `einvoicing.einvoice.not-retryable` | 409 | `*myle.EinvoicingEinvoiceNotRetryableError` |
| `einvoicing.einvoice.credit-note-reference-missing` | 409 | `*myle.EinvoicingEinvoiceCreditNoteReferenceMissingError` |
| `einvoicing.incoming-einvoice.already-settled` | 409 | `*myle.EinvoicingIncomingEinvoiceAlreadySettledError` |
| `einvoicing.incoming-einvoice.not-on-platform` | 409 | `*myle.EinvoicingIncomingEinvoiceNotOnPlatformError` |
| `einvoicing.incoming-einvoice.not-accepted` | 409 | `*myle.EinvoicingIncomingEinvoiceNotAcceptedError` |
| `payments.account.not-connected` | 409 | `*myle.PaymentAccountNotConnectedError` |
| `payments.account.demo` | 409 | `*myle.PaymentAccountDemoError` |
| `payments.account.suspended` | 409 | `*myle.PaymentAccountSuspendedError` |
| `payments.collection.disabled` | 409 | `*myle.PaymentCollectionDisabledError` |
| `payments.collection.paused` | 409 | `*myle.PaymentCollectionPausedError` |
| `payments.plan.collection-required` | 402 | `*myle.PaymentPlanCollectionRequiredError` |
| `payments.mandate.missing` | 409 | `*myle.PaymentMandateMissingError` |
| `payments.mandate.foreign-iban` | 409 | `*myle.PaymentMandateForeignIbanError` |
| `payments.mandate.invitations-throttled` | 429 | `*myle.PaymentMandateInvitationsThrottledError` |
| `payments.debit.not-debitable` | 409 | `*myle.PaymentDebitNotDebitableError` |
| `payments.debit.nothing-due` | 409 | `*myle.PaymentDebitNothingDueError` |
| `payments.debit.below-minimum` | 409 | `*myle.PaymentDebitBelowMinimumError` |
| `payments.debit.above-maximum` | 409 | `*myle.PaymentDebitAboveMaximumError` |
| `payments.debit.quota-reached` | 409 | `*myle.PaymentDebitQuotaReachedError` |
| `payments.refund.nothing-to-refund` | 409 | `*myle.PaymentRefundNothingToRefundError` |

## Retries, concurrency, versions

- Retries (`WithMaxRetries`, 2 by default): network errors and 5xx when the call is safe to repeat (`GET`,
  `PUT`, `DELETE`, or a write with an idempotency key); 429 always, after `Retry-After` (seconds or an HTTP
  date; one above 60 s is not waited for, the error is returned with `RetryAfter`); backoff with jitter.
- Every single resource has `ETag()` and `LastResponse()`. Pass it to `myle.IfMatch` on `Update` / `Delete`:
  if someone changed it since, the call returns `*myle.CorePreconditionFailedError` (412) and changes nothing.
- Each SDK release pins the contract date it was generated against (`myle.APIVersion`, `2026-11-21` here)
  and sends it as `Myle-API-Version`: an API change never changes what this code receives. When the API
  answers an operation with a deprecated behavior (`Deprecation`, `Sunset`), the deprecation handler is
  called once per operation (a line on the standard logger by default).

## Methods

### `client.Rights`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx)` | `GET /rights` | List the rights |

### `client.Products`

| Method | HTTP | Summary |
|---|---|---|
| `Search(ctx, params)` | `GET /products` | Search the products across organizations |

### `client.Companies`

| Method | HTTP | Summary |
|---|---|---|
| `Search(ctx, params)` | `GET /companies` | Search the companies across organizations |

### `client.Invoices`

| Method | HTTP | Summary |
|---|---|---|
| `Search(ctx, params)` | `GET /invoices` | Search the invoices across organizations |

### `client.Quotations`

| Method | HTTP | Summary |
|---|---|---|
| `Search(ctx, params)` | `GET /quotations` | Search the quotations across organizations |

### `client.Orders`

| Method | HTTP | Summary |
|---|---|---|
| `Search(ctx, params)` | `GET /orders` | Search the orders across organizations |

### `client.Me`

| Method | HTTP | Summary |
|---|---|---|
| `Get(ctx)` | `GET /me` | Get the signed-in user |

### `client.Me.APIKeys`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx)` | `GET /me/api-keys` | List my API keys |
| `Create(ctx, params)` | `POST /me/api-keys` | Create a user key |
| `Get(ctx, apiKeyID, params)` | `GET /me/api-keys/{apiKeyId}` | Get one of my API keys |
| `Revoke(ctx, apiKeyID)` | `DELETE /me/api-keys/{apiKeyId}` | Revoke one of my API keys |
| `Update(ctx, apiKeyID, params)` | `PATCH /me/api-keys/{apiKeyId}` | Change one of my API keys |
| `Rotate(ctx, apiKeyID)` | `POST /me/api-keys/{apiKeyId}/rotate` | Rotate one of my API keys |

### `client.Organization(id)`

| Method | HTTP | Summary |
|---|---|---|
| `Get(ctx)` | `GET /organizations/{id}` | Get an organization |

### `client.Organization(id).APIKeys`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx)` | `GET /organizations/{id}/api-keys` | List the organization's API keys |
| `Create(ctx, params)` | `POST /organizations/{id}/api-keys` | Create an organization key |
| `Get(ctx, apiKeyID)` | `GET /organizations/{id}/api-keys/{apiKeyId}` | Get one of the organization's API keys |
| `Revoke(ctx, apiKeyID)` | `DELETE /organizations/{id}/api-keys/{apiKeyId}` | Revoke one of the organization's API keys |
| `Update(ctx, apiKeyID, params)` | `PATCH /organizations/{id}/api-keys/{apiKeyId}` | Change one of the organization's API keys |
| `Rotate(ctx, apiKeyID)` | `POST /organizations/{id}/api-keys/{apiKeyId}/rotate` | Rotate one of the organization's API keys |

### `client.Organization(id).Products`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, params)` | `GET /organizations/{id}/products` | List the products |
| `Create(ctx, params)` | `POST /organizations/{id}/products` | Create a product |
| `ListTags(ctx)` | `GET /organizations/{id}/products/tags` | List the products' tags |
| `Get(ctx, productID)` | `GET /organizations/{id}/products/{productId}` | Get a product |
| `Delete(ctx, productID)` | `DELETE /organizations/{id}/products/{productId}` | Delete a product |
| `Update(ctx, productID, params)` | `PATCH /organizations/{id}/products/{productId}` | Change a product |
| `GetNeighbors(ctx, productID, params)` | `GET /organizations/{id}/products/{productId}/neighbors` | Get a product's neighbors in the list |

### `client.Organization(id).Products.Barcodes`

| Method | HTTP | Summary |
|---|---|---|
| `Create(ctx, productID, params)` | `POST /organizations/{id}/products/{productId}/barcodes` | Add a barcode to a product |
| `Get(ctx, productID, barcodeID)` | `GET /organizations/{id}/products/{productId}/barcodes/{barcodeId}` | Get a barcode of a product |
| `Delete(ctx, productID, barcodeID)` | `DELETE /organizations/{id}/products/{productId}/barcodes/{barcodeId}` | Remove a barcode from a product |
| `Update(ctx, productID, barcodeID, params)` | `PATCH /organizations/{id}/products/{productId}/barcodes/{barcodeId}` | Change a barcode of a product |

### `client.Organization(id).Companies`

| Method | HTTP | Summary |
|---|---|---|
| `ListTags(ctx)` | `GET /organizations/{id}/companies/tags` | List the companies' tags |
| `GetNeighbors(ctx, companyID, params)` | `GET /organizations/{id}/companies/{companyId}/neighbors` | Get a company's neighbors in a list |
| `List(ctx, params)` | `GET /organizations/{id}/companies` | List the companies |
| `Create(ctx, params)` | `POST /organizations/{id}/companies` | Create a company |
| `Get(ctx, companyID)` | `GET /organizations/{id}/companies/{companyId}` | Get a company |
| `Delete(ctx, companyID)` | `DELETE /organizations/{id}/companies/{companyId}` | Delete a company |
| `Update(ctx, companyID, params)` | `PATCH /organizations/{id}/companies/{companyId}` | Change a company |
| `GetEinvoicingRouting(ctx, companyID)` | `GET /organizations/{id}/companies/{companyId}/einvoicing-routing` | Get a customer's e-invoicing routing |
| `ReplaceEinvoicingRouting(ctx, companyID, params)` | `PUT /organizations/{id}/companies/{companyId}/einvoicing-routing` | Choose a customer's electronic address |
| `GetSepaMandate(ctx, companyID)` | `GET /organizations/{id}/companies/{companyId}/sepa-mandate` | Get a customer's SEPA mandate |

### `client.Organization(id).Companies.EmailAddresses`

| Method | HTTP | Summary |
|---|---|---|
| `Create(ctx, companyID, params)` | `POST /organizations/{id}/companies/{companyId}/email-addresses` | Add an e-mail address to a company |
| `Delete(ctx, companyID, companyEmailAddressID)` | `DELETE /organizations/{id}/companies/{companyId}/email-addresses/{companyEmailAddressId}` | Remove an e-mail address from a company |
| `Update(ctx, companyID, companyEmailAddressID, params)` | `PATCH /organizations/{id}/companies/{companyId}/email-addresses/{companyEmailAddressId}` | Change an e-mail address of a company |
| `Resubscribe(ctx, companyID, companyEmailAddressID)` | `POST /organizations/{id}/companies/{companyId}/email-addresses/{companyEmailAddressId}/resubscribe` | Subscribe an e-mail address of a company again |

### `client.Organization(id).Companies.PostalAddresses`

| Method | HTTP | Summary |
|---|---|---|
| `Create(ctx, companyID, params)` | `POST /organizations/{id}/companies/{companyId}/postal-addresses` | Add a postal address to a company |
| `Get(ctx, companyID, postalAddressID)` | `GET /organizations/{id}/companies/{companyId}/postal-addresses/{postalAddressId}` | Get a postal address of a company |
| `Delete(ctx, companyID, postalAddressID)` | `DELETE /organizations/{id}/companies/{companyId}/postal-addresses/{postalAddressId}` | Remove a postal address from a company |
| `Update(ctx, companyID, postalAddressID, params)` | `PATCH /organizations/{id}/companies/{companyId}/postal-addresses/{postalAddressId}` | Change a postal address of a company |

### `client.Organization(id).Companies.SepaMandate`

| Method | HTTP | Summary |
|---|---|---|
| `CreateInvitation(ctx, companyID, params)` | `POST /organizations/{id}/companies/{companyId}/sepa-mandate/invitation` | Invite a customer to sign a SEPA mandate |
| `Revoke(ctx, companyID)` | `POST /organizations/{id}/companies/{companyId}/sepa-mandate/revoke` | Revoke a customer's SEPA mandate |

### `client.Organization(id).Invoices`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, params)` | `GET /organizations/{id}/invoices` | List the invoices |
| `Create(ctx, params)` | `POST /organizations/{id}/invoices` | Create a draft invoice |
| `ListTags(ctx)` | `GET /organizations/{id}/invoices/tags` | List the invoices' tags |
| `GetEligibility(ctx, params)` | `GET /organizations/{id}/invoices/eligibility` | Count what each batch would act on |
| `Get(ctx, invoiceID)` | `GET /organizations/{id}/invoices/{invoiceId}` | Get an invoice |
| `DownloadPDF(ctx, invoiceID, params)` | `GET /organizations/{id}/invoices/{invoiceId}` (`Accept: application/pdf`) | Get an invoice as a PDF file |
| `Delete(ctx, invoiceID)` | `DELETE /organizations/{id}/invoices/{invoiceId}` | Delete a draft invoice |
| `Update(ctx, invoiceID, params)` | `PATCH /organizations/{id}/invoices/{invoiceId}` | Change a draft invoice |
| `GetNeighbors(ctx, invoiceID, params)` | `GET /organizations/{id}/invoices/{invoiceId}/neighbors` | Get an invoice's neighbors in the list |
| `Validate(ctx, invoiceID)` | `POST /organizations/{id}/invoices/{invoiceId}/validate` | Validate an invoice |
| `BatchValidate(ctx, params)` | `POST /organizations/{id}/invoices/batch-validate` | Validate several invoices |
| `BatchShare(ctx, params)` | `POST /organizations/{id}/invoices/batch-share` | Mark several invoices as handed to their customers |
| `BatchCreatePayments(ctx, params)` | `POST /organizations/{id}/invoices/batch-payments` | Record payments on several invoices |
| `BatchCreateCreditNotes(ctx, params)` | `POST /organizations/{id}/invoices/batch-credit-notes` | Cancel several invoices with credit notes |
| `BatchDuplicate(ctx, params)` | `POST /organizations/{id}/invoices/batch-duplicate` | Duplicate several invoices as drafts |
| `Send(ctx, invoiceID, params)` | `POST /organizations/{id}/invoices/{invoiceId}/send` | Send an invoice by e-mail |
| `Edit(ctx, invoiceID, params)` | `POST /organizations/{id}/invoices/{invoiceId}/edit` | Change a draft's lines, customer or description |
| `GetEditability(ctx, invoiceID)` | `GET /organizations/{id}/invoices/{invoiceId}/editability` | Tell whether a draft can still be changed |
| `Share(ctx, invoiceID, params)` | `POST /organizations/{id}/invoices/{invoiceId}/share` | Mark an invoice as handed to its customer |
| `MarkLost(ctx, invoiceID)` | `POST /organizations/{id}/invoices/{invoiceId}/mark-lost` | Write an invoice off as lost |
| `Unpay(ctx, invoiceID)` | `POST /organizations/{id}/invoices/{invoiceId}/unpay` | Take an invoice back to unpaid |
| `Reinvoice(ctx, invoiceID)` | `POST /organizations/{id}/invoices/{invoiceId}/reinvoice` | Re-bill a credit note as a new draft |
| `SetCorrectedInvoice(ctx, invoiceID, params)` | `POST /organizations/{id}/invoices/{invoiceId}/set-corrected-invoice` | Name the invoice a credit note corrects |
| `GetEinvoice(ctx, invoiceID)` | `GET /organizations/{id}/invoices/{invoiceId}/einvoice` | Get an invoice's e-invoice |
| `SubmitEinvoice(ctx, invoiceID)` | `POST /organizations/{id}/invoices/{invoiceId}/submit-einvoice` | Submit an invoice through the e-invoicing network |
| `Debit(ctx, invoiceID)` | `POST /organizations/{id}/invoices/{invoiceId}/debit` | Debit an invoice by SEPA |
| `Refund(ctx, invoiceID)` | `POST /organizations/{id}/invoices/{invoiceId}/refund` | Refund an invoice's Stripe payments |
| `GetPaymentStatus(ctx, invoiceID)` | `GET /organizations/{id}/invoices/{invoiceId}/payment-status` | Get an invoice's payment status |

### `client.Organization(id).Invoices.CreditNotes`

| Method | HTTP | Summary |
|---|---|---|
| `Create(ctx, invoiceID)` | `POST /organizations/{id}/invoices/{invoiceId}/credit-notes` | Cancel an invoice with a credit note |

### `client.Organization(id).Invoices.CorrectableInvoices`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, invoiceID, params)` | `GET /organizations/{id}/invoices/{invoiceId}/correctable-invoices` | List the invoices a credit note can correct |

### `client.Organization(id).Invoices.StatusHistory`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, invoiceID)` | `GET /organizations/{id}/invoices/{invoiceId}/status-history` | List an invoice's status history |

### `client.Organization(id).Invoices.Payments`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, invoiceID)` | `GET /organizations/{id}/invoices/{invoiceId}/payments` | List an invoice's payments |
| `Create(ctx, invoiceID, params)` | `POST /organizations/{id}/invoices/{invoiceId}/payments` | Record a payment on an invoice |
| `Get(ctx, invoiceID, paymentID)` | `GET /organizations/{id}/invoices/{invoiceId}/payments/{paymentId}` | Get a payment of an invoice |
| `Delete(ctx, invoiceID, paymentID)` | `DELETE /organizations/{id}/invoices/{invoiceId}/payments/{paymentId}` | Remove a payment of an invoice |

### `client.Organization(id).Quotations`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, params)` | `GET /organizations/{id}/quotations` | List the quotations |
| `Create(ctx, params)` | `POST /organizations/{id}/quotations` | Create a quotation |
| `ListTags(ctx)` | `GET /organizations/{id}/quotations/tags` | List the quotations' tags |
| `GetEligibility(ctx, params)` | `GET /organizations/{id}/quotations/eligibility` | Count what each batch would act on |
| `Get(ctx, quotationID)` | `GET /organizations/{id}/quotations/{quotationId}` | Get a quotation |
| `DownloadPDF(ctx, quotationID)` | `GET /organizations/{id}/quotations/{quotationId}` (`Accept: application/pdf`) | Get a quotation as a PDF file |
| `Delete(ctx, quotationID)` | `DELETE /organizations/{id}/quotations/{quotationId}` | Delete a quotation |
| `Update(ctx, quotationID, params)` | `PATCH /organizations/{id}/quotations/{quotationId}` | Change a quotation |
| `GetNeighbors(ctx, quotationID, params)` | `GET /organizations/{id}/quotations/{quotationId}/neighbors` | Get a quotation's neighbors in the list |
| `Edit(ctx, quotationID, params)` | `POST /organizations/{id}/quotations/{quotationId}/edit` | Change a quotation's lines, customer or description |
| `GetEditability(ctx, quotationID)` | `GET /organizations/{id}/quotations/{quotationId}/editability` | Tell whether a quotation can still be changed |
| `Share(ctx, quotationID)` | `POST /organizations/{id}/quotations/{quotationId}/share` | Mark a quotation as handed to its customer |
| `Approve(ctx, quotationID)` | `POST /organizations/{id}/quotations/{quotationId}/approve` | Record that the customer accepted a quotation |
| `BatchShare(ctx, params)` | `POST /organizations/{id}/quotations/batch-share` | Mark several quotations as handed to their customers |
| `BatchApprove(ctx, params)` | `POST /organizations/{id}/quotations/batch-approve` | Record that customers accepted several quotations |
| `BatchOrder(ctx, params)` | `POST /organizations/{id}/quotations/batch-order` | Turn several accepted quotations into orders |
| `Cancel(ctx, quotationID)` | `POST /organizations/{id}/quotations/{quotationId}/cancel` | Cancel a quotation (refused or withdrawn) |
| `Extend(ctx, quotationID, params)` | `POST /organizations/{id}/quotations/{quotationId}/extend` | Extend a quotation's validity |
| `Order(ctx, quotationID)` | `POST /organizations/{id}/quotations/{quotationId}/order` | Turn an accepted quotation into an order |
| `Invoice(ctx, quotationID)` | `POST /organizations/{id}/quotations/{quotationId}/invoice` | Bill an accepted quotation |
| `Send(ctx, quotationID, params)` | `POST /organizations/{id}/quotations/{quotationId}/send` | Send a quotation by e-mail |

### `client.Organization(id).Quotations.StatusHistory`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, quotationID)` | `GET /organizations/{id}/quotations/{quotationId}/status-history` | List a quotation's status history |

### `client.Organization(id).Orders`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, params)` | `GET /organizations/{id}/orders` | List the orders |
| `Create(ctx, params)` | `POST /organizations/{id}/orders` | Create an order |
| `ListTags(ctx)` | `GET /organizations/{id}/orders/tags` | List the orders' tags |
| `GetEligibility(ctx, params)` | `GET /organizations/{id}/orders/eligibility` | Count what each batch would act on |
| `Get(ctx, orderID)` | `GET /organizations/{id}/orders/{orderId}` | Get an order |
| `DownloadPDF(ctx, orderID)` | `GET /organizations/{id}/orders/{orderId}` (`Accept: application/pdf`) | Get an order as a PDF file |
| `Delete(ctx, orderID)` | `DELETE /organizations/{id}/orders/{orderId}` | Delete an order |
| `Update(ctx, orderID, params)` | `PATCH /organizations/{id}/orders/{orderId}` | Change an order |
| `GetNeighbors(ctx, orderID, params)` | `GET /organizations/{id}/orders/{orderId}/neighbors` | Get an order's neighbors in the list |
| `Edit(ctx, orderID, params)` | `POST /organizations/{id}/orders/{orderId}/edit` | Change an order's lines, customer, delivery address or description |
| `GetEditability(ctx, orderID)` | `GET /organizations/{id}/orders/{orderId}/editability` | Tell whether an order can still be changed |
| `Share(ctx, orderID)` | `POST /organizations/{id}/orders/{orderId}/share` | Mark an order as handed to its customer |
| `Approve(ctx, orderID)` | `POST /organizations/{id}/orders/{orderId}/approve` | Record that the customer confirmed an order |
| `MarkWaitingForDelivery(ctx, orderID)` | `POST /organizations/{id}/orders/{orderId}/mark-waiting-for-delivery` | Mark an order as prepared, waiting for its delivery |
| `Cancel(ctx, orderID)` | `POST /organizations/{id}/orders/{orderId}/cancel` | Cancel an order |
| `BatchShare(ctx, params)` | `POST /organizations/{id}/orders/batch-share` | Mark several orders as handed to their customers |
| `BatchApprove(ctx, params)` | `POST /organizations/{id}/orders/batch-approve` | Record that customers confirmed several orders |
| `GetStockAvailability(ctx, orderID)` | `GET /organizations/{id}/orders/{orderId}/stock-availability` | List the stocks an order's goods can leave from |
| `Deliver(ctx, orderID, params)` | `POST /organizations/{id}/orders/{orderId}/deliver` | Deliver an order: make its delivery note |
| `Invoice(ctx, orderID, params)` | `POST /organizations/{id}/orders/{orderId}/invoice` | Bill an order |
| `Send(ctx, orderID, params)` | `POST /organizations/{id}/orders/{orderId}/send` | Send an order by e-mail |

### `client.Organization(id).Orders.StatusHistory`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, orderID)` | `GET /organizations/{id}/orders/{orderId}/status-history` | List an order's status history |

### `client.Organization(id).DeliveryNotes`

| Method | HTTP | Summary |
|---|---|---|
| `Get(ctx, deliveryNoteID)` | `GET /organizations/{id}/delivery-notes/{deliveryNoteId}` | Get a delivery note |

### `client.Organization(id).Einvoicing`

| Method | HTTP | Summary |
|---|---|---|
| `Get(ctx)` | `GET /organizations/{id}/einvoicing` | Get the organization's e-invoicing |
| `Connect(ctx)` | `POST /organizations/{id}/einvoicing/connect` | Start connecting the organization to SuperPDP |
| `Disconnect(ctx)` | `POST /organizations/{id}/einvoicing/disconnect` | Disconnect the organization from SuperPDP |
| `Sync(ctx)` | `POST /organizations/{id}/einvoicing/sync` | Read the verification statuses from SuperPDP |
| `GetAddress(ctx)` | `GET /organizations/{id}/einvoicing/address` | Get the organization's electronic address |
| `ReplaceAddress(ctx, params)` | `PUT /organizations/{id}/einvoicing/address` | Choose the organization's electronic address |

### `client.Organization(id).Einvoices`

| Method | HTTP | Summary |
|---|---|---|
| `ListOutgoingFailures(ctx)` | `GET /organizations/{id}/einvoices/outgoing-failures` | List the failed submissions |

### `client.Organization(id).IncomingEinvoices`

| Method | HTTP | Summary |
|---|---|---|
| `List(ctx, params)` | `GET /organizations/{id}/incoming-einvoices` | List the incoming e-invoices |
| `Get(ctx, incomingEinvoiceID)` | `GET /organizations/{id}/incoming-einvoices/{incomingEinvoiceId}` | Get an incoming e-invoice |
| `DownloadPDF(ctx, incomingEinvoiceID)` | `GET /organizations/{id}/incoming-einvoices/{incomingEinvoiceId}` (`Accept: application/pdf`) | Get an incoming e-invoice as a PDF file |
| `DownloadXML(ctx, incomingEinvoiceID)` | `GET /organizations/{id}/incoming-einvoices/{incomingEinvoiceId}` (`Accept: application/xml`) | Get an incoming e-invoice as a XML file |
| `Accept(ctx, incomingEinvoiceID)` | `POST /organizations/{id}/incoming-einvoices/{incomingEinvoiceId}/accept` | Accept an incoming e-invoice |
| `Refuse(ctx, incomingEinvoiceID, params)` | `POST /organizations/{id}/incoming-einvoices/{incomingEinvoiceId}/refuse` | Refuse an incoming e-invoice |
| `Dispute(ctx, incomingEinvoiceID, params)` | `POST /organizations/{id}/incoming-einvoices/{incomingEinvoiceId}/dispute` | Dispute an incoming e-invoice |
| `AcceptPartially(ctx, incomingEinvoiceID, params)` | `POST /organizations/{id}/incoming-einvoices/{incomingEinvoiceId}/accept-partially` | Accept an incoming e-invoice in part |
| `MarkPaid(ctx, incomingEinvoiceID, params)` | `POST /organizations/{id}/incoming-einvoices/{incomingEinvoiceId}/mark-paid` | Mark an incoming e-invoice paid |

### `client.Organization(id).PaymentsAccount`

| Method | HTTP | Summary |
|---|---|---|
| `Get(ctx)` | `GET /organizations/{id}/payments-account` | Get the organization's Stripe account |
| `StartOnboarding(ctx, params)` | `POST /organizations/{id}/payments-account/onboarding` | Start or resume the Stripe onboarding |
| `CreateDashboardLink(ctx)` | `POST /organizations/{id}/payments-account/dashboard-link` | Open the Stripe dashboard |
| `GetOnlinePaymentSetting(ctx)` | `GET /organizations/{id}/payments-account/online-payment` | Get the online payment of invoices |
| `ReplaceOnlinePaymentSetting(ctx, params)` | `PUT /organizations/{id}/payments-account/online-payment` | Turn the online payment of invoices on or off |
| `ListSepaFees(ctx, params)` | `GET /organizations/{id}/payments-account/sepa-fees` | List the SEPA debits' fees |
| `GetSepaQuota(ctx)` | `GET /organizations/{id}/payments-account/sepa-quota` | Get the SEPA quota |
