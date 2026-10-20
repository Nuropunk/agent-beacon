package dashboard

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/localonly"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

func withEdition(t *testing.T, edition string) {
	t.Helper()
	prev := version.Edition
	version.Edition = edition
	t.Cleanup(func() { version.Edition = prev })
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestNuroEditionServesThemedPages(t *testing.T) {
	withEdition(t, localonly.Edition)
	h, err := Handler(Options{UserMode: true, LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/session.html", "/findings.html", "/inventory-hooks.html"} {
		rec := get(t, h, http.MethodGet, path)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, `<html lang="en" data-theme="nuro">`) ||
			!strings.Contains(body, `<link rel="stylesheet" href="/theme-nuro.css" />`) {
			t.Errorf("%s: status %d, not themed", path, rec.Code)
		}
		if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
			t.Errorf("%s: themed page lost the security headers (CSP %q)", path, got)
		}
	}
	if rec := get(t, h, http.MethodGet, "/theme-nuro.css"); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Errorf("theme sheet: status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := get(t, h, http.MethodGet, "/index.html"); rec.Code != http.StatusMovedPermanently {
		t.Errorf("/index.html: status %d, want the file server's redirect to /", rec.Code)
	}
	if rec := get(t, h, http.MethodHead, "/"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD /: status %d, body %d bytes", rec.Code, rec.Body.Len())
	}
	if rec := get(t, h, http.MethodGet, "/missing.html"); rec.Code != http.StatusNotFound {
		t.Errorf("/missing.html: status %d, want 404", rec.Code)
	}
}

func TestCommunityEditionServesUpstreamPages(t *testing.T) {
	withEdition(t, version.DefaultEdition)
	h, err := Handler(Options{UserMode: true, LogPath: filepath.Join(t.TempDir(), "runtime.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	if body := get(t, h, http.MethodGet, "/").Body.String(); strings.Contains(body, "data-theme") || strings.Contains(body, "theme-nuro.css") {
		t.Error("a community build served the nuro theme")
	}
	if strings.Contains(string(lensPrelude()), nuroLensTokensCSS) {
		t.Error("a community build themed the lenses")
	}
}

// The theme is applied by two exact string replacements; a page whose markup drifts from them
// would silently render unthemed.
func TestEveryPageTakesTheTheme(t *testing.T) {
	root, err := fs.Sub(staticFiles, "static")
	if err != nil {
		t.Fatal(err)
	}
	pages, _ := fs.Glob(root, "*.html")
	if len(pages) == 0 {
		t.Fatal("no static pages")
	}
	for _, name := range pages {
		page, _ := fs.ReadFile(root, name)
		themed := string(themePage(page, nuroTheme))
		if !strings.Contains(themed, `data-theme="nuro"`) || !strings.Contains(themed, "/theme-nuro.css") {
			t.Errorf("%s does not take the theme", name)
		}
	}
}

// The theme must stay local: no remote fonts, imports or URLs, and fonts only by local().
func TestNuroThemeSheetStaysLocal(t *testing.T) {
	sheet, err := staticFiles.ReadFile("static/theme-nuro.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(sheet)
	for _, banned := range []string{"://", "@import", "url("} {
		if strings.Contains(css, banned) {
			t.Errorf("theme-nuro.css contains %q", banned)
		}
	}
	for _, src := range regexp.MustCompile(`src:[^;]*;`).FindAllString(css, -1) {
		if strings.Contains(strings.ReplaceAll(src, "local(", ""), "(") {
			t.Errorf("font source %q is not local()", src)
		}
	}
	for _, banned := range []string{"Inter", "Roboto", "Arial", "Orbitron"} {
		if strings.Contains(css, banned) {
			t.Errorf("theme-nuro.css names the font %s", banned)
		}
	}
}

func TestNuroEditionThemesLenses(t *testing.T) {
	withEdition(t, localonly.Edition)
	prelude := string(lensPrelude())
	tokens := strings.Index(prelude, lensTokensCSS)
	theme := strings.Index(prelude, nuroLensTokensCSS)
	if tokens < 0 || theme < tokens {
		t.Fatalf("the lens theme must follow the spec tokens (tokens at %d, theme at %d)", tokens, theme)
	}
}
