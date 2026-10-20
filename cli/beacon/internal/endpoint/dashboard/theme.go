package dashboard

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/localonly"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

// nuroTheme is the theme the nuro edition serves: static/theme-nuro.css on every page, and
// nuroLensTokensCSS after the lens tokens.
const nuroTheme = "nuro"

// dashboardTheme returns the theme this build serves, or "" for upstream's look.
func dashboardTheme() string {
	if version.Edition == localonly.Edition {
		return nuroTheme
	}
	return ""
}

// nuroLensTokensCSS restyles lenses through the --beacon-* tokens every lens already reads. It is
// appended after tokens.css, which stays byte-identical to the spec.
const nuroLensTokensCSS = `:root {
  color-scheme: dark;
  --beacon-text: #e2e8f0;
  --beacon-muted: #8a9bb0;
  --beacon-line: rgba(64, 192, 208, 0.18);
  --beacon-panel-soft: #0f1624;
  --beacon-accent: #40c0d0;
  --beacon-accent-soft: rgba(64, 192, 208, 0.14);
  --beacon-danger: #ff6b81;
  --beacon-warn: #f4c430;
  --beacon-ok: #4fd1a5;
  --beacon-font-sans: "JetBrains Mono", "Fira Code", ui-monospace, monospace;
  --beacon-font-mono: "JetBrains Mono", "Fira Code", ui-monospace, monospace;
}
html { background: #0a0e18; color: #e2e8f0; }
`

// themedPages serves the dashboard's HTML pages marked with theme and linked to its sheet, and
// hands every other request to next.
func themedPages(root fs.FS, theme string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		// /index.html itself is left to the file server, which redirects it to /.
		if r.URL.Path == "/index.html" || !strings.HasSuffix(name, ".html") || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			next.ServeHTTP(w, r)
			return
		}
		page, err := fs.ReadFile(root, name)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(themePage(page, theme))
	})
}

// themePage marks page's <html> element with data-theme and links the theme's sheet after
// styles.css, so the theme's rules win on equal specificity.
func themePage(page []byte, theme string) []byte {
	page = bytes.Replace(page, []byte(`<html lang="en">`), []byte(`<html lang="en" data-theme="`+theme+`">`), 1)
	sheet := `<link rel="stylesheet" href="/styles.css" />`
	return bytes.Replace(page, []byte(sheet), []byte(sheet+"\n    "+`<link rel="stylesheet" href="/theme-`+theme+`.css" />`), 1)
}
