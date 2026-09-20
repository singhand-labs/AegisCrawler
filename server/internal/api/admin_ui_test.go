package api

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
)

func TestAdminUIRedirectsToSlash(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/admin/" {
		t.Fatalf("expected redirect to /admin/, got %s", loc)
	}
}

func TestAdminUIIndexPage(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	client := srv.Client()
	resp, err := client.Get(srv.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	_, embedErr := fs.ReadFile(adminFS, "index.html")
	if embedErr == nil {
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `<div id="root"></div>`) {
			t.Fatalf("expected SPA root element in index.html, got %s", string(body))
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			t.Fatalf("expected text/html content type, got %s", ct)
		}
	} else {
		// When the admin UI has not been built, a placeholder page is served.
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 when admin UI is not built, got %d", resp.StatusCode)
		}
	}
}

func TestAdminUIServesStaticAssets(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	entries, err := fs.ReadDir(adminFS, "assets")
	if err != nil {
		t.Skipf("admin UI assets not built: %v", err)
	}

	var jsFile string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".js") {
			jsFile = e.Name()
			break
		}
	}
	if jsFile == "" {
		t.Skip("no JS asset found to test")
	}

	client := srv.Client()
	resp, err := client.Get(srv.URL + "/admin/assets/" + jsFile)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for asset %s, got %d", jsFile, resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "javascript") && !strings.Contains(ct, "octet-stream") {
		t.Fatalf("expected javascript content type for asset, got %s", ct)
	}
}

func TestAdminUIMissingAssetReturns404(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	client := srv.Client()
	resp, err := client.Get(srv.URL + "/admin/assets/does-not-exist.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing asset, got %d", resp.StatusCode)
	}
}

func TestAdminUISPAFallback(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	if _, err := fs.ReadFile(adminFS, "index.html"); err != nil {
		t.Skipf("admin UI not built, skipping SPA fallback test: %v", err)
	}

	client := srv.Client()
	// Use a path that does not collide with any /admin API route.
	resp, err := client.Get(srv.URL + "/admin/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for client-side route, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `<div id="root"></div>`) {
		t.Fatalf("expected index.html fallback for client-side route")
	}
}

func TestAdminUINavigationToAPIRouteReturnsSPA(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	if _, err := fs.ReadFile(adminFS, "index.html"); err != nil {
		t.Skipf("admin UI not built, skipping SPA fallback test: %v", err)
	}

	client := srv.Client()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/admin/rules/rule-123", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a browser page navigation.
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Sec-Fetch-Dest", "document")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for browser navigation to API route, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `<div id="root"></div>`) {
		t.Fatalf("expected index.html fallback when browser navigates to /admin/rules/{id}")
	}
}

func TestAdminUIFetchToAPIRouteNotInterceptedBySPA(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	client := srv.Client()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/admin/rules/rule-123", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an SPA fetch/XHR request (not a top-level navigation).
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Sec-Fetch-Dest", "empty")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	// The request must reach the admin API, not be served index.html.
	if strings.Contains(bodyStr, `<div id="root"></div>`) {
		t.Fatalf("SPA fallback should not intercept API fetch")
	}
	// Missing rule should yield a JSON 404 from the API.
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing rule API fetch, got %d", resp.StatusCode)
	}
	if !strings.Contains(bodyStr, "rule not found") {
		t.Fatalf("expected rule not found error for API fetch, got %s", bodyStr)
	}
}
