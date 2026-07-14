package linuxmetrics

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestParseCPU(t *testing.T) {
	got, err := parseCPU(strings.NewReader("cpu  100 20 30 400 10 5 6 7 8 9\ncpu0 1 2 3 4\n"))
	if err != nil {
		t.Fatalf("parseCPU() error = %v", err)
	}
	want := CPUCounters{Total: 595, Idle: 410}
	if got != want {
		t.Fatalf("parseCPU() = %#v, want %#v", got, want)
	}
}

func TestParseCPURejectsMalformedAndOverflowingInput(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
	}{
		{name: "missing aggregate", fixture: "cpu0 1 2 3 4\n"},
		{name: "too few fields", fixture: "cpu 1 2 3\n"},
		{name: "invalid number", fixture: "cpu 1 x 3 4\n"},
		{name: "negative number", fixture: "cpu 1 2 3 -4\n"},
		{name: "sum overflow", fixture: "cpu " + strconv.FormatUint(math.MaxUint64, 10) + " 1 0 0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseCPU(strings.NewReader(tt.fixture)); err == nil {
				t.Fatal("parseCPU() error = nil, want non-nil")
			}
		})
	}
}

func TestParseMemoryUsesMemAvailable(t *testing.T) {
	fixture := "MemTotal: 1000 kB\nMemAvailable: 250 kB\nHugePages_Total: 0\n"
	total, used, err := parseMemory(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("parseMemory() error = %v", err)
	}
	if total != 1_024_000 || used != 768_000 {
		t.Fatalf("parseMemory() = total %d used %d", total, used)
	}
}

func TestParseMemoryUsesOlderKernelFallback(t *testing.T) {
	fixture := strings.Join([]string{
		"MemTotal: 1000 kB",
		"MemFree: 100 kB",
		"Buffers: 50 kB",
		"Cached: 200 kB",
		"SReclaimable: 20 kB",
		"Shmem: 10 kB",
	}, "\n")
	total, used, err := parseMemory(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("parseMemory() error = %v", err)
	}
	if total != 1_024_000 || used != 655_360 {
		t.Fatalf("parseMemory() = total %d used %d", total, used)
	}
}

func TestParseMemoryClampsAvailableToTotal(t *testing.T) {
	fixture := "MemTotal: 100 kB\nMemAvailable: 200 kB\n"
	total, used, err := parseMemory(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if total != 102_400 || used != 0 {
		t.Fatalf("parseMemory() = total %d used %d", total, used)
	}
}

func TestParseMemoryRejectsMissingInvalidAndOverflowingValues(t *testing.T) {
	overflowKB := strconv.FormatUint(uint64(math.MaxInt64/1024)+1, 10)
	tests := []struct {
		name    string
		fixture string
	}{
		{name: "missing total", fixture: "MemAvailable: 1 kB\n"},
		{name: "wrong unit", fixture: "MemTotal: 100 MB\nMemAvailable: 50 kB\n"},
		{name: "invalid number", fixture: "MemTotal: many kB\nMemAvailable: 50 kB\n"},
		{name: "negative number", fixture: "MemTotal: -1 kB\nMemAvailable: 0 kB\n"},
		{name: "overflow", fixture: "MemTotal: " + overflowKB + " kB\nMemAvailable: 0 kB\n"},
		{name: "incomplete fallback", fixture: "MemTotal: 100 kB\nMemFree: 10 kB\nBuffers: 10 kB\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := parseMemory(strings.NewReader(tt.fixture)); err == nil {
				t.Fatal("parseMemory() error = nil, want non-nil")
			}
		})
	}
}

func TestParseLoad(t *testing.T) {
	load1, load5, load15, err := parseLoad(strings.NewReader("0.10 0.20 0.30 1/100 123\n"))
	if err != nil {
		t.Fatalf("parseLoad() error = %v", err)
	}
	if load1 != 0.1 || load5 != 0.2 || load15 != 0.3 {
		t.Fatalf("parseLoad() = %v/%v/%v", load1, load5, load15)
	}
}

func TestParseLoadRejectsInvalidValues(t *testing.T) {
	for _, fixture := range []string{"0.1 0.2\n", "x 0.2 0.3\n", "-0.1 0.2 0.3\n", "NaN 0.2 0.3\n", "+Inf 0.2 0.3\n"} {
		if _, _, _, err := parseLoad(strings.NewReader(fixture)); err == nil {
			t.Fatalf("parseLoad(%q) error = nil", fixture)
		}
	}
}

func TestParseNetworkExcludesLoopback(t *testing.T) {
	fixture := "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n" +
		" lo: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n" +
		"eth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\n" +
		" wlan0: 3000 0 0 0 0 0 0 0 4000 0 0 0 0 0 0 0\n"
	got, err := parseNetwork(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("parseNetwork() error = %v", err)
	}
	want := NetworkCounters{RXBytes: 4000, TXBytes: 6000}
	if got != want {
		t.Fatalf("parseNetwork() = %#v, want %#v", got, want)
	}
}

func TestParseNetworkAllowsLoopbackOnly(t *testing.T) {
	fixture := "lo: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n"
	got, err := parseNetwork(strings.NewReader(fixture))
	if err != nil || got != (NetworkCounters{}) {
		t.Fatalf("parseNetwork() = %#v, error %v", got, err)
	}
}

func TestParseNetworkRejectsMalformedAndOverflowingInput(t *testing.T) {
	max := strconv.FormatUint(math.MaxUint64, 10)
	tests := []string{
		"eth0 1 2 3\n",
		"eth0: 1 2 3\n",
		"eth0: bad 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0\n",
		"eth0: " + max + " 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0\neth1: 1 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0\n",
	}
	for _, fixture := range tests {
		if _, err := parseNetwork(strings.NewReader(fixture)); err == nil {
			t.Fatalf("parseNetwork(%q) error = nil", fixture)
		}
	}
}

func TestParseUptime(t *testing.T) {
	got, err := parseUptime(strings.NewReader("123.99 40.0\n"))
	if err != nil || got != 123 {
		t.Fatalf("parseUptime() = %d, error %v", got, err)
	}
}

func TestParseUptimeRejectsInvalidValues(t *testing.T) {
	for _, fixture := range []string{"", "bad 1\n", "-1 1\n", "NaN 1\n", "+Inf 1\n", "1e30 1\n"} {
		if _, err := parseUptime(strings.NewReader(fixture)); err == nil {
			t.Fatalf("parseUptime(%q) error = nil", fixture)
		}
	}
}
