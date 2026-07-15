package deploytest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWebAppThresholdOrderAssetContract(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	indexHTML, err := os.ReadFile(filepath.Join(repositoryRoot, "internal", "webapp", "assets", "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	appJS, err := os.ReadFile(filepath.Join(repositoryRoot, "internal", "webapp", "assets", "app.js"))
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	for _, required := range []string{`id="offline-threshold"`, `id="alert-threshold"`} {
		if !strings.Contains(string(indexHTML), required) {
			t.Errorf("index.html missing threshold contract %q", required)
		}
	}
	for _, required := range []string{
		"syncThresholdConstraint",
		`alert.min = offline.value || "1"`,
		"alert.setCustomValidity",
		`offline.addEventListener("input", syncThresholdConstraint)`,
		`alert.addEventListener("input", syncThresholdConstraint)`,
	} {
		if !strings.Contains(string(appJS), required) {
			t.Errorf("app.js missing threshold contract %q", required)
		}
	}
}
