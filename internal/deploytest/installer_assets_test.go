package deploytest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerReleaseAndChineseDocumentationContracts(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	required := map[string][]string{
		"scripts/manage.sh": {
			"安装或升级 Server", "安装或升级 Agent", "同机安装或升级 Server + Agent",
			"卸载 Server", "卸载 Agent", "卸载全部组件", "SHA256SUMS",
			"set-menu-button", "reset-menu-button", "彻底删除", "0600",
		},
		".github/workflows/release.yml": {
			"tags:", "v*", "go test ./...", "CGO_ENABLED=0", "scripts/build-server.sh",
			"scripts/build-agent.sh", "SHA256SUMS", "gh release create",
		},
		"README.md": {
			"tg-monitor", "简体中文", "sudo bash scripts/manage.sh", "普通卸载", "彻底卸载",
			"打开监控面板", "8443",
		},
	}
	for name, fragments := range required {
		contents, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(contents), fragment) {
				t.Errorf("%s missing %q", name, fragment)
			}
		}
	}
}
