package webapp

import (
	"os"
	"strings"
	"testing"
)

func TestAssetsHonorTelegramBootstrapAndSecretStorageContract(t *testing.T) {
	html := readAsset(t, "assets/index.html")
	javascript := readAsset(t, "assets/app.js")

	telegramBridge := strings.Index(html, "https://telegram.org/js/telegram-web-app.js")
	applicationScript := strings.Index(html, "/app/app.js")
	if telegramBridge < 0 || applicationScript < 0 || telegramBridge > applicationScript {
		t.Fatal("Telegram bridge must load before the application script")
	}
	for _, forbidden := range []string{"initDataUnsafe", "localStorage", "sessionStorage", "indexedDB", "innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "console."} {
		if strings.Contains(javascript, forbidden) {
			t.Errorf("app.js contains forbidden %q", forbidden)
		}
	}
	for _, required := range []string{
		"Telegram.WebApp.initData", "Telegram.WebApp.ready", "Telegram.WebApp.expand",
		`credentials: "same-origin"`, "visibilityState", "15000", "AbortController",
		"textContent", "createElementNS", "/api/v1/auth/session", "/api/v1/auth/telegram",
		"/api/v1/auth/logout", "/api/v1/admin/overview", "/api/v1/admin/settings",
		"/api/v1/admin/alert-preference", "/rotate-token", "/history?",
	} {
		if !strings.Contains(javascript, required) {
			t.Errorf("app.js missing %q", required)
		}
	}
}

func TestHTMLProvidesSemanticOperatorControls(t *testing.T) {
	html := readAsset(t, "assets/index.html")
	for _, required := range []string{
		"<header", "<nav", "<main", "<section", "<form", "<dialog",
		`aria-live="polite"`, `aria-live="assertive"`, `id="token-dialog"`,
		`id="token-copy"`, `id="token-close"`, `id="logout-button"`,
		`data-range="1h"`, `data-range="6h"`, `data-range="24h"`, `data-range="7d"`,
		`name="offline_threshold_seconds"`, `name="alert_threshold_seconds"`,
		`name="history_retention_days"`, `name="alert_preference"`,
		`name="delete_confirmation"`, `autocomplete="off"`,
	} {
		if !strings.Contains(html, required) {
			t.Errorf("index.html missing %q", required)
		}
	}
	if strings.Contains(html, "<script>") || strings.Contains(html, "onclick=") {
		t.Error("index.html must not contain inline scripts or event handlers")
	}
}

func TestCSSProvidesResponsiveAccessibleTheme(t *testing.T) {
	stylesheet := readAsset(t, "assets/app.css")
	for _, required := range []string{
		"--app-bg", "--app-text", "--app-accent", "var(--tg-theme-bg-color",
		":focus-visible", "min-height: 44px", "prefers-reduced-motion: reduce",
		"@media (max-width: 320px)", "color-scheme: light dark", "dialog::backdrop",
	} {
		if !strings.Contains(stylesheet, required) {
			t.Errorf("app.css missing %q", required)
		}
	}
}

func readAsset(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}
