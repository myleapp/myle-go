package myle_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	myle "github.com/myleapp/myle-go"
)

func ExampleNewClient() {
	client := myle.NewClient(myle.WithAPIKey(os.Getenv("MYLE_API_KEY")), myle.WithMaxRetries(3))
	me, err := client.Me.Get(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(me.Email)
}

func ExampleProductsService_List() {
	org := myle.NewClient().Organization("organization_…")
	params := &myle.ProductListParams{Status: []myle.ProductStatus{myle.ProductStatusActive}}
	for product, err := range org.Products.List(context.Background(), params).All() {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(product.Number, product.Name)
	}
}

func ExampleProductsService_Update() {
	ctx := context.Background()
	org := myle.NewClient().Organization("organization_…")
	product, err := org.Products.Get(ctx, "product_…")
	if err != nil {
		log.Fatal(err)
	}
	updated, err := org.Products.Update(ctx, product.ID, &myle.ProductUpdateParams{
		Name:             myle.String("Foin de luzerne"),
		AccountingNumber: myle.Null[string](),
	}, myle.IfMatch(product))
	var stale *myle.CorePreconditionFailedError
	if errors.As(err, &stale) {
		fmt.Println("changed since: read it again")
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(updated.ETag())
}

func ExampleAPIError() {
	org := myle.NewClient().Organization("organization_…")
	_, err := org.Products.Barcodes.Create(context.Background(), "product_…", &myle.ProductBarcodeCreateParams{
		Value:  "3017620422003",
		Format: myle.BarcodeFormatEan13,
	})
	var duplicate *myle.ProductBarcodeDuplicateError
	var apiErr *myle.APIError
	switch {
	case errors.As(err, &duplicate):
		fmt.Println("already there:", duplicate.Context.Value)
	case errors.As(err, &apiErr):
		fmt.Println(apiErr.Status, apiErr.Code, apiErr.Message, apiErr.RequestID)
	}
}

func ExampleInvoicesService_DownloadPDF() {
	org := myle.NewClient().Organization("organization_…")
	file, err := org.Invoices.DownloadPDF(context.Background(), "invoice_…", &myle.GetInvoicePDFParams{
		Format: myle.Value(myle.GetInvoicePDFFormatFacturx),
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := file.Save(cmp.Or(file.Filename, "invoice.pdf")); err != nil {
		log.Fatal(err)
	}
}
