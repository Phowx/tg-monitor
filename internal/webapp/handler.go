package webapp

import (
	"embed"
	"net/http"
	"strconv"
)

const contentSecurityPolicy = "default-src 'none'; script-src 'self' https://telegram.org; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self' https://web.telegram.org https://*.telegram.org"

//go:embed assets/index.html assets/app.css assets/app.js
var files embed.FS

type handler struct{}

func NewHandler() http.Handler {
	return handler{}
}

func (handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch request.URL.Path {
	case "/app":
		http.Redirect(writer, request, "/app/", http.StatusPermanentRedirect)
	case "/app/":
		serveAsset(writer, request, "assets/index.html", "text/html; charset=utf-8", "no-store")
	case "/app/app.css":
		serveAsset(writer, request, "assets/app.css", "text/css; charset=utf-8", "public, max-age=31536000, immutable")
	case "/app/app.js":
		serveAsset(writer, request, "assets/app.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable")
	default:
		http.NotFound(writer, request)
	}
}

func serveAsset(writer http.ResponseWriter, request *http.Request, name, contentType, cacheControl string) {
	contents, err := files.ReadFile(name)
	if err != nil {
		http.Error(writer, "internal server error", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", cacheControl)
	writer.Header().Set("Content-Length", strconv.Itoa(len(contents)))
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = writer.Write(contents)
	}
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
}
