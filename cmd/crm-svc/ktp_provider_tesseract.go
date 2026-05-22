// Picker variant compiled when the `tesseract` build tag is set —
// resolves the "tesseract" name to the actual provider.

//go:build tesseract

package main

import crmhttp "github.com/ion-core/backend/internal/crm/adapter/http"

func pickKTPProvider(name string) crmhttp.KTPProvider {
	switch name {
	case "tesseract":
		return crmhttp.NewTesseractProvider()
	}
	return nil
}
