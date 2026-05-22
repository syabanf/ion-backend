package main

import (
	"net/http"
	"strings"
)

// stripPrefix strips a prefix from r.URL.Path before invoking next.
// chi.Mount already strips for chi routes, but reverse-proxy handlers
// see the full URL; this brings them in line.
func stripPrefix(prefix string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := strings.TrimPrefix(r.URL.Path, prefix); p != r.URL.Path {
			r2 := r.Clone(r.Context())
			r2.URL.Path = p
			if r2.URL.Path == "" {
				r2.URL.Path = "/"
			}
			next.ServeHTTP(w, r2)
			return
		}
		next.ServeHTTP(w, r)
	})
}
