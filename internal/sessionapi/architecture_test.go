package sessionapi

import (
	"os"
	"strings"
	"testing"
)

func TestSessionAPIDoesNotImportConcreteSQLiteStorage(t *testing.T) {
	for _, path := range []string{"handler.go", "lifecycle.go"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		if strings.Contains(string(content), "internal/storage/sqlite") {
			t.Fatalf("%s imports concrete SQLite storage", path)
		}
	}
}
