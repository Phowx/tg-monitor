package webapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const expectedContentSecurityPolicy = "default-src 'none'; script-src 'self' https://telegram.org; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self' https://web.telegram.org https://*.telegram.org"

func TestHandlerRedirectsCanonicalAppPath(t *testing.T) {
	response := serveWebApp(http.MethodGet, "/app")
	if response.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308; body=%q", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/app/" {
		t.Fatalf("Location = %q, want /app/", location)
	}
	assertSecurityHeaders(t, response)
}

func TestHandlerServesOnlyAllowlistedAssets(t *testing.T) {
	tests := []struct {
		path        string
		contentType string
		cache       string
		contains    string
	}{
		{path: "/app/", contentType: "text/html; charset=utf-8", cache: "no-store", contains: "Telegram Monitor"},
		{path: "/app/app.css", contentType: "text/css; charset=utf-8", cache: "public, max-age=31536000, immutable", contains: "--app-bg"},
		{path: "/app/app.js", contentType: "text/javascript; charset=utf-8", cache: "public, max-age=31536000, immutable", contains: "Telegram.WebApp"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := serveWebApp(http.MethodGet, test.path)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			if got := response.Header().Get("Cache-Control"); got != test.cache {
				t.Fatalf("Cache-Control = %q, want %q", got, test.cache)
			}
			if !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("body does not contain %q", test.contains)
			}
			assertSecurityHeaders(t, response)
		})
	}

	for _, path := range []string{"/", "/app/index.html", "/app/assets", "/app/app.js.map", "/app/unknown", "/app//"} {
		t.Run("not found "+path, func(t *testing.T) {
			response := serveWebApp(http.MethodGet, path)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body=%q", response.Code, response.Body.String())
			}
			assertSecurityHeaders(t, response)
		})
	}
}

func TestHandlerSupportsHeadAndRejectsOtherMethods(t *testing.T) {
	for _, path := range []string{"/app/", "/app/app.css", "/app/app.js"} {
		response := serveWebApp(http.MethodHead, path)
		if response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("HEAD %s = %d body=%q", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Content-Length") == "" {
			t.Fatalf("HEAD %s omitted Content-Length", path)
		}
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		response := serveWebApp(method, "/app/")
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405", method, response.Code)
		}
		if allow := response.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Fatalf("%s Allow = %q, want GET, HEAD", method, allow)
		}
		assertSecurityHeaders(t, response)
	}
}

func serveWebApp(method, target string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(method, target, nil))
	return response
}

func assertSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	wants := map[string]string{
		"Content-Security-Policy": expectedContentSecurityPolicy,
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
	}
	for name, want := range wants {
		if got := response.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
