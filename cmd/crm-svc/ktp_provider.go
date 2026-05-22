// Default KTP provider picker — returns nil for any name except stub.
// The `tesseract` build tag swaps in the version that knows about
// crmhttp.NewTesseractProvider.

//go:build !tesseract

package main

import crmhttp "github.com/ion-core/backend/internal/crm/adapter/http"

func pickKTPProvider(name string) crmhttp.KTPProvider {
	// Without the tesseract build tag, only the stub is available.
	// We deliberately return nil here so cmd/crm-svc/main.go falls
	// back to the stub with a warning that the requested provider
	// isn't compiled in.
	_ = name
	return nil
}
