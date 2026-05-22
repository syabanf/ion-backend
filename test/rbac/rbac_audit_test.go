// Package rbac is a meta-test that scans each bounded context's HTTP handler
// for route registrations + the permission middleware that guards each.
// It produces a human-readable audit and fails the build if any mutation
// endpoint (POST/PUT/PATCH/DELETE) is unguarded.
//
// Run with:
//
//	make rbac-audit
//
// or directly:
//
//	go test ./test/rbac -v -tags=rbac
package rbac

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Handlers we know to scan. Each entry is a logical context + the relative
// path to its handler. The test walks each file looking for chi route
// registrations preceded (or wrapped) by a RequirePermission middleware.
var handlers = []struct {
	context string
	path    string
}{
	{"identity", "../../internal/identity/adapter/http/handler.go"},
	{"network", "../../internal/network/adapter/http/handler.go"},
	{"warehouse", "../../internal/warehouse/adapter/http/handler.go"},
	{"crm", "../../internal/crm/adapter/http/handler.go"},
	{"field", "../../internal/field/adapter/http/handler.go"},
	{"billing", "../../internal/billing/adapter/http/handler.go"},
}

// =====================================================================
// Regex pipeline
//
// We look for chi method calls (Get, Post, Patch, Put, Delete, Head, Mount).
// A line like:
//
//	r.With(httpserver.RequirePermission("foo.bar")).Get("/x", h.x)
//
// or split across lines via the standard
//
//	r.With(httpserver.RequirePermission("foo.bar")).
//	    Get("/x", h.x)
//
// pattern is what the codebase uses. We normalize by removing newlines
// inside chi-builder chains before regex matching.
// =====================================================================

// joinChainedCalls collapses `).\n    Method(` patterns into `).Method(` so
// a fluent registration spread across two lines fits on one logical line.
//
// The continuation must start with a verb method name (`Get`, `Post`, …)
// or `With` — without that anchor, the regex would happily swallow the
// newline that follows a harmless `).` in a comment, silently hiding the
// next real route from the audit. Anchoring on the method name keeps
// the join restricted to actual chained calls.
var chainRe = regexp.MustCompile(`\)\s*\.\s*\n\s*(Get|Post|Patch|Put|Delete|Head|With)\(`)

// lineCommentRe strips `// …` line comments before normalization so that
// trailing comments end-of-line never participate in chain detection or
// confuse the route regex. Block comments stay because the chain regex's
// method-name anchor already protects against them.
var lineCommentRe = regexp.MustCompile(`(?m)//.*$`)

func normalize(src string) string {
	src = lineCommentRe.ReplaceAllString(src, "")
	return chainRe.ReplaceAllString(src, ").$1(")
}

// routeRe captures the method + the path. The permission (if any) is
// captured separately.
var routeRe = regexp.MustCompile(`(?m)^\s*r(?:\.With\(.+?\))?\.(Get|Post|Patch|Put|Delete|Head)\(\s*"([^"]+)"`)
var permRe = regexp.MustCompile(`RequirePermission\(\s*"([^"]+)"\s*\)`)

type route struct {
	context string
	method  string
	path    string
	perm    string // empty = unguarded
}

func (r route) String() string {
	p := r.perm
	if p == "" {
		p = "(none)"
	}
	return fmt.Sprintf("%-9s %-6s %-40s  %s", r.context, r.method, r.path, p)
}

// extractRoutes pulls one route per call site.
func extractRoutes(ctx, src string) []route {
	src = normalize(src)
	lines := strings.Split(src, "\n")
	out := []route{}
	for _, l := range lines {
		m := routeRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		method := strings.ToUpper(m[1])
		path := m[2]
		perm := ""
		if pm := permRe.FindStringSubmatch(l); pm != nil {
			perm = pm[1]
		}
		out = append(out, route{context: ctx, method: method, path: path, perm: perm})
	}
	return out
}

func loadRoutes(t *testing.T) []route {
	t.Helper()
	all := []route{}
	for _, h := range handlers {
		abs, err := filepath.Abs(h.path)
		if err != nil {
			t.Fatalf("abs %s: %v", h.path, err)
		}
		buf, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("read %s: %v", abs, err)
		}
		rs := extractRoutes(h.context, string(buf))
		if len(rs) == 0 {
			t.Errorf("no routes extracted from %s — regex may have drifted", abs)
		}
		all = append(all, rs...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].context != all[j].context {
			return all[i].context < all[j].context
		}
		if all[i].path != all[j].path {
			return all[i].path < all[j].path
		}
		return all[i].method < all[j].method
	})
	return all
}

// TestRBACAudit prints the route → permission map and fails on any
// unguarded mutation endpoint (POST/PUT/PATCH/DELETE).
//
// The only exempt route is identity-svc's /auth/login + refresh, which
// must be reachable without a token. We allow any handler under
// /auth/* to be unguarded — that's the surface area for un-authed
// requests by design.
func TestRBACAudit(t *testing.T) {
	routes := loadRoutes(t)

	fmt.Println()
	fmt.Println("RBAC route audit")
	fmt.Println("================")
	fmt.Printf("%-9s %-6s %-40s  %s\n", "context", "method", "path", "permission")
	fmt.Println(strings.Repeat("-", 90))
	for _, r := range routes {
		fmt.Println(r)
	}
	fmt.Println()

	unguarded := 0
	for _, r := range routes {
		if r.perm != "" {
			continue
		}
		// Mutations on /auth/* are by design unguarded — login/refresh.
		if strings.HasPrefix(r.path, "/auth/") {
			continue
		}
		// /portal/* is the public customer-facing surface — OTP-gated,
		// per-IP rate-limited, no JWT. Intentionally unguarded by RBAC.
		if strings.HasPrefix(r.path, "/portal/") {
			continue
		}
		// /auth/me is exempt — it's "who am I", needs auth but no permission.
		// Other GETs may be intentionally unguarded too; only mutations are blocked.
		if r.method == "GET" || r.method == "HEAD" {
			continue
		}
		t.Errorf("unguarded mutation: %s %s %s", r.context, r.method, r.path)
		unguarded++
	}
	if unguarded == 0 {
		t.Logf("OK — %d routes, no unguarded mutations", len(routes))
	}
}
