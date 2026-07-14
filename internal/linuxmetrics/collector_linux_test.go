package linuxmetrics

import (
	"context"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

var collectorFixturePaths = []string{
	"/proc/stat",
	"/proc/meminfo",
	"/proc/loadavg",
	"/proc/net/dev",
	"/proc/uptime",
}

func collectorFixtures() map[string]string {
	return map[string]string{
		"/proc/stat":    "cpu 100 20 30 400 10 5 5 30\n",
		"/proc/meminfo": "MemTotal: 1000 kB\nMemAvailable: 250 kB\n",
		"/proc/loadavg": "0.10 0.20 0.30 1/100 123\n",
		"/proc/net/dev": "Inter-| Receive | Transmit\n eth0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n",
		"/proc/uptime":  "3600.90 100.00\n",
	}
}

type recordingReadCloser struct {
	reader  io.Reader
	closed  *bool
	readErr error
}

type readerFunc func([]byte) (int, error)

func (function readerFunc) Read(buffer []byte) (int, error) { return function(buffer) }

func (r *recordingReadCloser) Read(buffer []byte) (int, error) {
	if r.readErr != nil {
		return 0, r.readErr
	}
	return r.reader.Read(buffer)
}

func (r *recordingReadCloser) Close() error {
	*r.closed = true
	return nil
}

func fixtureDependencies(fixtures map[string]string) (collectorDependencies, *[]string, map[string]*bool) {
	var opened []string
	closed := make(map[string]*bool, len(fixtures))
	dependencies := collectorDependencies{
		open: func(path string) (io.ReadCloser, error) {
			value, ok := fixtures[path]
			if !ok {
				return nil, errors.New("unexpected path")
			}
			opened = append(opened, path)
			wasClosed := false
			closed[path] = &wasClosed
			return &recordingReadCloser{reader: strings.NewReader(value), closed: &wasClosed}, nil
		},
		statFS: func(string) (fileSystemStats, error) {
			return fileSystemStats{Blocks: 1_000, Bfree: 250, Bsize: 4_096}, nil
		},
		hostname: func() (string, error) { return "host-a", nil },
		kernel:   func() (string, error) { return "6.12.0", nil },
		arch:     func() string { return "amd64" },
		now:      func() time.Time { return time.Unix(1_700_000_000, 123_000_000) },
	}
	return dependencies, &opened, closed
}

func TestCollectorCapturesInjectedLinuxHost(t *testing.T) {
	dependencies, opened, closed := fixtureDependencies(collectorFixtures())

	got, err := newCollector(dependencies).Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	want := Snapshot{
		CapturedAt:         time.Unix(1_700_000_000, 123_000_000),
		CPU:                CPUCounters{Total: 600, Idle: 410},
		MemoryTotalBytes:   1_024_000,
		MemoryUsedBytes:    768_000,
		RootDiskTotalBytes: 4_096_000,
		RootDiskUsedBytes:  3_072_000,
		Load1:              0.1,
		Load5:              0.2,
		Load15:             0.3,
		Network:            NetworkCounters{RXBytes: 100, TXBytes: 200},
		UptimeSeconds:      3_600,
		System:             systemInfo("host-a", "6.12.0", "amd64"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Capture() = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(*opened, collectorFixturePaths) {
		t.Fatalf("opened paths = %#v, want %#v", *opened, collectorFixturePaths)
	}
	for _, path := range collectorFixturePaths {
		if wasClosed := closed[path]; wasClosed == nil || !*wasClosed {
			t.Errorf("%s was not closed", path)
		}
	}
}

func TestCollectorRejectsSourceErrors(t *testing.T) {
	parserFailures := map[string]string{
		"/proc/stat":    "not cpu\n",
		"/proc/meminfo": "MemTotal: 0 kB\n",
		"/proc/loadavg": "bad\n",
		"/proc/net/dev": "eth0 malformed\n",
		"/proc/uptime":  "bad\n",
	}
	for path, invalid := range parserFailures {
		t.Run("parse "+path, func(t *testing.T) {
			fixtures := collectorFixtures()
			fixtures[path] = invalid
			dependencies, _, _ := fixtureDependencies(fixtures)
			if _, err := newCollector(dependencies).Capture(context.Background()); err == nil {
				t.Fatal("Capture() error = nil, want parser error")
			}
		})
	}

	for _, failingPath := range collectorFixturePaths {
		t.Run("open "+failingPath, func(t *testing.T) {
			dependencies, _, _ := fixtureDependencies(collectorFixtures())
			original := dependencies.open
			dependencies.open = func(path string) (io.ReadCloser, error) {
				if path == failingPath {
					return nil, errors.New("open failed")
				}
				return original(path)
			}
			if _, err := newCollector(dependencies).Capture(context.Background()); err == nil {
				t.Fatal("Capture() error = nil, want open error")
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(*collectorDependencies)
	}{
		{name: "statfs", mutate: func(dependencies *collectorDependencies) {
			dependencies.statFS = func(string) (fileSystemStats, error) { return fileSystemStats{}, errors.New("statfs failed") }
		}},
		{name: "hostname", mutate: func(dependencies *collectorDependencies) {
			dependencies.hostname = func() (string, error) { return "", errors.New("hostname failed") }
		}},
		{name: "kernel", mutate: func(dependencies *collectorDependencies) {
			dependencies.kernel = func() (string, error) { return "", errors.New("kernel failed") }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _, _ := fixtureDependencies(collectorFixtures())
			test.mutate(&dependencies)
			if _, err := newCollector(dependencies).Capture(context.Background()); err == nil {
				t.Fatal("Capture() error = nil, want dependency error")
			}
		})
	}
}

func TestCollectorHonorsContextCancellation(t *testing.T) {
	t.Run("before capture", func(t *testing.T) {
		dependencies, opened, _ := fixtureDependencies(collectorFixtures())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := newCollector(dependencies).Capture(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Capture() error = %v, want context.Canceled", err)
		}
		if len(*opened) != 0 {
			t.Fatalf("opened paths = %#v, want none", *opened)
		}
	})

	t.Run("during read", func(t *testing.T) {
		dependencies, _, _ := fixtureDependencies(collectorFixtures())
		ctx, cancel := context.WithCancel(context.Background())
		closed := false
		dependencies.open = func(string) (io.ReadCloser, error) {
			return &recordingReadCloser{
				reader: readerFunc(func(buffer []byte) (int, error) {
					cancel()
					return copy(buffer, "cpu 1 1 1 1\n"), io.EOF
				}),
				closed: &closed,
			}, nil
		}
		if _, err := newCollector(dependencies).Capture(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Capture() error = %v, want context.Canceled", err)
		}
		if !closed {
			t.Fatal("reader was not closed")
		}
	})
}

func TestCollectorReadBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		limit int64
	}{
		{name: "stat", limit: maxStatBytes}, {name: "memory", limit: maxMemoryBytes},
		{name: "load", limit: maxLoadBytes}, {name: "network", limit: maxNetworkBytes},
		{name: "uptime", limit: maxUptimeBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			limit := test.limit
			if got, err := readBounded(context.Background(), strings.NewReader(strings.Repeat("x", int(limit))), limit); err != nil || int64(len(got)) != limit {
				t.Fatalf("readBounded(exact) length = %d, error = %v", len(got), err)
			}
			if _, err := readBounded(context.Background(), strings.NewReader(strings.Repeat("x", int(limit+1))), limit); err == nil {
				t.Fatal("readBounded(over) error = nil")
			}
		})
	}
}

func TestCollectorRejectsInvalidIdentityAndDiskOverflow(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*collectorDependencies)
	}{
		{name: "empty hostname", mutate: func(dependencies *collectorDependencies) {
			dependencies.hostname = func() (string, error) { return "  ", nil }
		}},
		{name: "empty kernel", mutate: func(dependencies *collectorDependencies) {
			dependencies.kernel = func() (string, error) { return "", nil }
		}},
		{name: "empty arch", mutate: func(dependencies *collectorDependencies) { dependencies.arch = func() string { return "" } }},
		{name: "disk total overflow", mutate: func(dependencies *collectorDependencies) {
			dependencies.statFS = func(string) (fileSystemStats, error) {
				return fileSystemStats{Blocks: math.MaxUint64, Bfree: 1, Bsize: 2}, nil
			}
		}},
		{name: "disk free exceeds total", mutate: func(dependencies *collectorDependencies) {
			dependencies.statFS = func(string) (fileSystemStats, error) { return fileSystemStats{Blocks: 1, Bfree: 2, Bsize: 1}, nil }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _, _ := fixtureDependencies(collectorFixtures())
			test.mutate(&dependencies)
			if _, err := newCollector(dependencies).Capture(context.Background()); err == nil {
				t.Fatal("Capture() error = nil, want validation error")
			}
		})
	}
}

func TestCollectorCapturesRealLinuxHost(t *testing.T) {
	snapshot, err := NewCollector().Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CPU.Total == 0 || snapshot.MemoryTotalBytes <= 0 || snapshot.RootDiskTotalBytes <= 0 || snapshot.System.OS != "linux" {
		t.Fatalf("invalid real snapshot: %#v", snapshot)
	}
}

func systemInfo(hostname, kernel, arch string) domain.SystemInfo {
	return domain.SystemInfo{Hostname: hostname, OS: "linux", Kernel: kernel, Arch: arch}
}
