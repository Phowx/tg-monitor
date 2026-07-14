package linuxmetrics

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
	"golang.org/x/sys/unix"
)

const (
	maxStatBytes    int64 = 1 << 20
	maxMemoryBytes  int64 = 64 << 10
	maxLoadBytes    int64 = 4 << 10
	maxNetworkBytes int64 = 1 << 20
	maxUptimeBytes  int64 = 4 << 10
)

type fileSystemStats struct {
	Blocks uint64
	Bfree  uint64
	Bsize  uint64
}

type collectorDependencies struct {
	open     func(string) (io.ReadCloser, error)
	statFS   func(string) (fileSystemStats, error)
	hostname func() (string, error)
	kernel   func() (string, error)
	arch     func() string
	now      func() time.Time
}

type Collector struct {
	dependencies collectorDependencies
}

func NewCollector() *Collector {
	return newCollector(collectorDependencies{
		open: func(path string) (io.ReadCloser, error) {
			return os.Open(path)
		},
		statFS: func(path string) (fileSystemStats, error) {
			var value unix.Statfs_t
			if err := unix.Statfs(path, &value); err != nil {
				return fileSystemStats{}, err
			}
			if value.Bsize < 0 {
				return fileSystemStats{}, errors.New("negative filesystem block size")
			}
			return fileSystemStats{Blocks: value.Blocks, Bfree: value.Bfree, Bsize: uint64(value.Bsize)}, nil
		},
		hostname: os.Hostname,
		kernel: func() (string, error) {
			var value unix.Utsname
			if err := unix.Uname(&value); err != nil {
				return "", err
			}
			return unix.ByteSliceToString(value.Release[:]), nil
		},
		arch: func() string { return runtime.GOARCH },
		now:  time.Now,
	})
}

func newCollector(dependencies collectorDependencies) *Collector {
	return &Collector{dependencies: dependencies}
}

func (c *Collector) Capture(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, errors.New("capture context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	stat, err := c.readFile(ctx, "/proc/stat", maxStatBytes)
	if err != nil {
		return Snapshot{}, err
	}
	cpu, err := parseCPU(bytes.NewReader(stat))
	if err != nil {
		return Snapshot{}, err
	}

	memory, err := c.readFile(ctx, "/proc/meminfo", maxMemoryBytes)
	if err != nil {
		return Snapshot{}, err
	}
	memoryTotal, memoryUsed, err := parseMemory(bytes.NewReader(memory))
	if err != nil {
		return Snapshot{}, err
	}

	load, err := c.readFile(ctx, "/proc/loadavg", maxLoadBytes)
	if err != nil {
		return Snapshot{}, err
	}
	load1, load5, load15, err := parseLoad(bytes.NewReader(load))
	if err != nil {
		return Snapshot{}, err
	}

	networkData, err := c.readFile(ctx, "/proc/net/dev", maxNetworkBytes)
	if err != nil {
		return Snapshot{}, err
	}
	network, err := parseNetwork(bytes.NewReader(networkData))
	if err != nil {
		return Snapshot{}, err
	}

	uptimeData, err := c.readFile(ctx, "/proc/uptime", maxUptimeBytes)
	if err != nil {
		return Snapshot{}, err
	}
	uptime, err := parseUptime(bytes.NewReader(uptimeData))
	if err != nil {
		return Snapshot{}, err
	}

	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	filesystem, err := c.dependencies.statFS("/")
	if err != nil {
		return Snapshot{}, fmt.Errorf("stat filesystem /: %w", err)
	}
	diskTotal, diskUsed, err := diskBytes(filesystem)
	if err != nil {
		return Snapshot{}, err
	}

	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	hostname, err := c.dependencies.hostname()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read hostname: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	kernel, err := c.dependencies.kernel()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read kernel release: %w", err)
	}
	hostname = strings.TrimSpace(hostname)
	kernel = strings.TrimSpace(kernel)
	arch := strings.TrimSpace(c.dependencies.arch())
	if hostname == "" || kernel == "" || arch == "" {
		return Snapshot{}, errors.New("host identity fields must be non-empty")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		CapturedAt:         c.dependencies.now(),
		CPU:                cpu,
		MemoryTotalBytes:   memoryTotal,
		MemoryUsedBytes:    memoryUsed,
		RootDiskTotalBytes: diskTotal,
		RootDiskUsedBytes:  diskUsed,
		Load1:              load1,
		Load5:              load5,
		Load15:             load15,
		Network:            network,
		UptimeSeconds:      uptime,
		System: domain.SystemInfo{
			Hostname: hostname,
			OS:       "linux",
			Kernel:   kernel,
			Arch:     arch,
		},
	}, nil
}

func (c *Collector) readFile(ctx context.Context, path string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reader, err := c.dependencies.open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	data, readErr := readBounded(ctx, reader, limit)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read %s: %w", path, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close %s: %w", path, closeErr)
	}
	return data, nil
}

func readBounded(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit == math.MaxInt64 {
		return nil, errors.New("read limit must be positive and bounded")
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("input exceeds %d-byte limit", limit)
	}
	return data, nil
}

func diskBytes(stats fileSystemStats) (int64, int64, error) {
	if stats.Bfree > stats.Blocks {
		return 0, 0, errors.New("filesystem free blocks exceed total blocks")
	}
	total, ok := checkedMultiply(stats.Blocks, stats.Bsize)
	if !ok || total == 0 || total > math.MaxInt64 {
		return 0, 0, errors.New("filesystem total bytes are outside report range")
	}
	free, ok := checkedMultiply(stats.Bfree, stats.Bsize)
	if !ok || free > total {
		return 0, 0, errors.New("filesystem free bytes are outside report range")
	}
	return int64(total), int64(total - free), nil
}

func checkedMultiply(left, right uint64) (uint64, bool) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, false
	}
	return left * right, true
}
