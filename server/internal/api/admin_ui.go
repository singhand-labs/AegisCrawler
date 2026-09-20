package api

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/singhand-labs/AegisCrawler/web"
)

// adminFS is the embedded admin UI file system rooted at the admin/ directory.
var adminFS, _ = fs.Sub(web.Files, "admin")

// AdminUIHandler serves the embedded React admin UI with SPA fallback.
// Static assets are served without authentication so the login page can load.
// API routes under /admin/ are registered separately and take precedence.
func AdminUIHandler() http.Handler {
	fileServer := http.FileServer(http.FS(adminFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect /admin to /admin/ so relative asset URLs resolve correctly.
		if r.URL.Path == "/admin" {
			http.Redirect(w, r, "/admin/", http.StatusFound)
			return
		}

		setAdminUISecurityHeaders(w)

		// Strip the /admin prefix and clean the path.
		cleanPath := path.Clean(strings.TrimPrefix(r.URL.Path, "/admin"))
		if cleanPath == "." {
			cleanPath = "/"
		}

		// Try to serve the requested file directly.
		fsPath := strings.TrimPrefix(cleanPath, "/")
		if fsPath == "" {
			fsPath = "."
		}

		f, err := adminFS.Open(fsPath)
		if err == nil {
			defer f.Close()
			stat, serr := f.Stat()
			if serr == nil && !stat.IsDir() {
				// Rewrite the request path so the file server looks up the file
				// inside the embedded admin/ root instead of under /admin/.
				r.URL.Path = cleanPath
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// If the request looks like an asset (has an extension) and was not found,
		// return a real 404 so missing bundles are visible in dev tools.
		if path.Ext(cleanPath) != "" {
			http.NotFound(w, r)
			return
		}

		// Otherwise serve index.html for client-side routing.
		serveIndexHTML(w, r)
	})
}

func serveIndexHTML(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(adminFS, "index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, adminUINotBuiltHTML)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
}

const adminUINotBuiltHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>AegisCrawler 管理后台未构建</title>
</head>
<body>
  <h1>管理后台未构建</h1>
  <p>请先在项目根目录执行 <code>npm run build:admin</code>，然后重新编译并启动服务。</p>
</body>
</html>`

// isStaticAdminAsset reports whether the request path looks like a static
// asset (js, css, image, font, etc.) that should be served by the file server.
func isStaticAdminAsset(p string) bool {
	ext := path.Ext(p)
	if ext == "" {
		return false
	}
	switch ext {
	case ".html", ".htm":
		return false
	case ".js", ".mjs", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".eot", ".otf", ".map", ".json", ".yaml", ".yml":
		return true
	default:
		return true
	}
}

// isBrowserNavigation reports whether the request is a browser page navigation
// (as opposed to an API/fetch request or a static asset load).
func isBrowserNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	// Modern browsers send Sec-Fetch-Dest: document for top-level navigations.
	if r.Header.Get("Sec-Fetch-Dest") == "document" {
		return true
	}
	// Fallback for older clients: Accept header starts with text/html.
	accept := r.Header.Get("Accept")
	if accept != "" && strings.HasPrefix(accept, "text/html") {
		return true
	}
	return false
}

// AdminUISPAFallbackMiddleware serves the embedded admin UI index.html for
// browser navigation requests under /admin/. This prevents refreshing on a
// client-side route like /admin/rules/{id} from hitting the admin API and
// returning a 401 JSON error. API calls from the SPA (fetch/XHR) are not
// affected because they do not have Sec-Fetch-Dest: document.
func AdminUISPAFallbackMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/") && !isStaticAdminAsset(r.URL.Path) && isBrowserNavigation(r) {
			setAdminUISecurityHeaders(w)
			serveIndexHTML(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setAdminUISecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	// Allow inline scripts/styles from the build and data URIs for Ant Design icons.
	w.Header().Set(
		"Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self' data:",
	)
}
