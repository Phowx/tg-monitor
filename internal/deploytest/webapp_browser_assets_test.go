package deploytest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWebAppBrowserSmokeContract(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))

	contents, err := os.ReadFile(filepath.Join(repositoryRoot, "scripts", "smoke-webapp.sh"))
	if err != nil {
		t.Fatalf("read scripts/smoke-webapp.sh: %v", err)
	}
	for _, required := range []string{
		"playwright-cli",
		"webapp-browser-test.js",
		"mobile-light.png",
		"mobile-dark.png",
		"desktop-admin.png",
		"console",
		"network",
		"secret_log_scan",
	} {
		if !strings.Contains(string(contents), required) {
			t.Errorf("browser smoke missing %q", required)
		}
	}
}

func TestREADMEContainsWebAppBrowserSmokeRunbook(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	contents, err := os.ReadFile(filepath.Join(repositoryRoot, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	for _, required := range []string{
		"scripts/smoke-webapp.sh",
		"playwright-cli",
		"output/playwright/webapp",
		"mobile-light.png",
		"mobile-dark.png",
		"desktop-admin.png",
		"webapp_browser=ok screenshots=3",
	} {
		if !strings.Contains(string(contents), required) {
			t.Errorf("README.md missing browser smoke runbook contract %q", required)
		}
	}
}
