package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const latestMetricColumns = `server_id, received_at_ms, captured_at_ms, cpu_pct,
	memory_total_bytes, memory_used_bytes, root_disk_total_bytes, root_disk_used_bytes,
	load_1, load_5, load_15, network_rx_total_bytes, network_tx_total_bytes,
	network_rx_bytes_per_second, network_tx_bytes_per_second, uptime_seconds,
	hostname, os, kernel, arch`

const latestMetricUpsert = `INSERT INTO latest_metrics(` + latestMetricColumns + `)
	VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(server_id) DO UPDATE SET
		received_at_ms = excluded.received_at_ms,
		captured_at_ms = excluded.captured_at_ms,
		cpu_pct = excluded.cpu_pct,
		memory_total_bytes = excluded.memory_total_bytes,
		memory_used_bytes = excluded.memory_used_bytes,
		root_disk_total_bytes = excluded.root_disk_total_bytes,
		root_disk_used_bytes = excluded.root_disk_used_bytes,
		load_1 = excluded.load_1,
		load_5 = excluded.load_5,
		load_15 = excluded.load_15,
		network_rx_total_bytes = excluded.network_rx_total_bytes,
		network_tx_total_bytes = excluded.network_tx_total_bytes,
		network_rx_bytes_per_second = excluded.network_rx_bytes_per_second,
		network_tx_bytes_per_second = excluded.network_tx_bytes_per_second,
		uptime_seconds = excluded.uptime_seconds,
		hostname = excluded.hostname,
		os = excluded.os,
		kernel = excluded.kernel,
		arch = excluded.arch`

func (s *Store) UpsertLatestMetrics(ctx context.Context, latest domain.LatestMetrics) error {
	if latest.ServerID <= 0 {
		return errors.New("upsert latest metrics: server ID must be positive")
	}
	if latest.ReceivedAtMS <= 0 {
		return errors.New("upsert latest metrics: received time must be positive")
	}
	if err := latest.Report.Validate(); err != nil {
		return fmt.Errorf("upsert latest metrics: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, latestMetricUpsert, latestMetricArgs(latest)...); err != nil {
		return fmt.Errorf("upsert latest metrics for server %d: %w", latest.ServerID, err)
	}
	return nil
}

func (s *Store) GetLatestMetrics(ctx context.Context, serverID int64) (domain.LatestMetrics, error) {
	latest, err := scanLatestMetrics(s.db.QueryRowContext(ctx, `SELECT `+latestMetricColumns+` FROM latest_metrics WHERE server_id = ?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LatestMetrics{}, fmt.Errorf("get latest metrics for server %d: %w", serverID, ErrNotFound)
	}
	if err != nil {
		return domain.LatestMetrics{}, fmt.Errorf("get latest metrics for server %d: %w", serverID, err)
	}
	return latest, nil
}

func (s *Store) ListLatestMetrics(ctx context.Context) ([]domain.LatestMetrics, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		l.server_id, l.received_at_ms, l.captured_at_ms, l.cpu_pct,
		l.memory_total_bytes, l.memory_used_bytes, l.root_disk_total_bytes, l.root_disk_used_bytes,
		l.load_1, l.load_5, l.load_15, l.network_rx_total_bytes, l.network_tx_total_bytes,
		l.network_rx_bytes_per_second, l.network_tx_bytes_per_second, l.uptime_seconds,
		l.hostname, l.os, l.kernel, l.arch
		FROM latest_metrics l JOIN servers s ON s.id = l.server_id
		ORDER BY s.sort_order, s.name, s.id`)
	if err != nil {
		return nil, fmt.Errorf("list latest metrics: %w", err)
	}
	defer rows.Close()

	latest := make([]domain.LatestMetrics, 0)
	for rows.Next() {
		item, err := scanLatestMetrics(rows)
		if err != nil {
			return nil, fmt.Errorf("list latest metrics: scan row: %w", err)
		}
		latest = append(latest, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list latest metrics: %w", err)
	}
	return latest, nil
}

func latestMetricArgs(latest domain.LatestMetrics) []any {
	report := latest.Report
	return []any{
		latest.ServerID, latest.ReceivedAtMS, report.CapturedAtMS, report.CPUPct,
		report.MemoryTotalBytes, report.MemoryUsedBytes, report.RootDiskTotalBytes, report.RootDiskUsedBytes,
		report.Load1, report.Load5, report.Load15, report.NetworkRXTotalBytes, report.NetworkTXTotalBytes,
		report.NetworkRXBytesPerSecond, report.NetworkTXBytesPerSecond, report.UptimeSeconds,
		report.System.Hostname, report.System.OS, report.System.Kernel, report.System.Arch,
	}
}

func scanLatestMetrics(scanner rowScanner) (domain.LatestMetrics, error) {
	var latest domain.LatestMetrics
	report := &latest.Report
	err := scanner.Scan(
		&latest.ServerID, &latest.ReceivedAtMS, &report.CapturedAtMS, &report.CPUPct,
		&report.MemoryTotalBytes, &report.MemoryUsedBytes, &report.RootDiskTotalBytes, &report.RootDiskUsedBytes,
		&report.Load1, &report.Load5, &report.Load15, &report.NetworkRXTotalBytes, &report.NetworkTXTotalBytes,
		&report.NetworkRXBytesPerSecond, &report.NetworkTXBytesPerSecond, &report.UptimeSeconds,
		&report.System.Hostname, &report.System.OS, &report.System.Kernel, &report.System.Arch,
	)
	return latest, err
}

const minuteSampleColumns = `server_id, bucket_ms, cpu_pct, memory_total_bytes, memory_used_bytes,
	root_disk_total_bytes, root_disk_used_bytes, load_1, load_5, load_15,
	network_rx_total_bytes, network_tx_total_bytes, network_rx_bytes_per_second,
	network_tx_bytes_per_second, uptime_seconds, hostname, os, kernel, arch`

const minuteSampleUpsert = `INSERT INTO metric_samples(` + minuteSampleColumns + `)
	VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(server_id, bucket_ms) DO UPDATE SET
		cpu_pct = excluded.cpu_pct,
		memory_total_bytes = excluded.memory_total_bytes,
		memory_used_bytes = excluded.memory_used_bytes,
		root_disk_total_bytes = excluded.root_disk_total_bytes,
		root_disk_used_bytes = excluded.root_disk_used_bytes,
		load_1 = excluded.load_1,
		load_5 = excluded.load_5,
		load_15 = excluded.load_15,
		network_rx_total_bytes = excluded.network_rx_total_bytes,
		network_tx_total_bytes = excluded.network_tx_total_bytes,
		network_rx_bytes_per_second = excluded.network_rx_bytes_per_second,
		network_tx_bytes_per_second = excluded.network_tx_bytes_per_second,
		uptime_seconds = excluded.uptime_seconds,
		hostname = excluded.hostname,
		os = excluded.os,
		kernel = excluded.kernel,
		arch = excluded.arch`

func (s *Store) UpsertMinuteSample(ctx context.Context, sample domain.MinuteSample) error {
	if err := validateMinuteSample(sample); err != nil {
		return fmt.Errorf("upsert minute sample: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, minuteSampleUpsert, minuteSampleArgs(sample)...); err != nil {
		return fmt.Errorf("upsert minute sample for server %d: %w", sample.ServerID, err)
	}
	return nil
}

func (s *Store) QueryMinuteSamples(ctx context.Context, serverID, fromMS, toMS int64) ([]domain.MinuteSample, error) {
	if toMS < fromMS {
		return nil, errors.New("query minute samples: end must not be before start")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+minuteSampleColumns+`
		FROM metric_samples WHERE server_id = ? AND bucket_ms >= ? AND bucket_ms < ?
		ORDER BY bucket_ms`, serverID, fromMS, toMS)
	if err != nil {
		return nil, fmt.Errorf("query minute samples for server %d: %w", serverID, err)
	}
	defer rows.Close()

	samples := make([]domain.MinuteSample, 0)
	for rows.Next() {
		sample, err := scanMinuteSample(rows)
		if err != nil {
			return nil, fmt.Errorf("query minute samples: scan row: %w", err)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query minute samples for server %d: %w", serverID, err)
	}
	return samples, nil
}

func (s *Store) DeleteMetricSamplesBefore(ctx context.Context, cutoffMS int64) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM metric_samples WHERE bucket_ms < ?`, cutoffMS)
	if err != nil {
		return 0, fmt.Errorf("delete metric samples before cutoff: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete metric samples before cutoff: read affected rows: %w", err)
	}
	return deleted, nil
}

func minuteSampleArgs(sample domain.MinuteSample) []any {
	return []any{
		sample.ServerID, sample.BucketMS, sample.CPUPct, sample.MemoryTotalBytes, sample.MemoryUsedBytes,
		sample.RootDiskTotalBytes, sample.RootDiskUsedBytes, sample.Load1, sample.Load5, sample.Load15,
		sample.NetworkRXTotalBytes, sample.NetworkTXTotalBytes, sample.NetworkRXBytesPerSecond,
		sample.NetworkTXBytesPerSecond, sample.UptimeSeconds, sample.System.Hostname, sample.System.OS,
		sample.System.Kernel, sample.System.Arch,
	}
}

func scanMinuteSample(scanner rowScanner) (domain.MinuteSample, error) {
	var sample domain.MinuteSample
	err := scanner.Scan(
		&sample.ServerID, &sample.BucketMS, &sample.CPUPct, &sample.MemoryTotalBytes, &sample.MemoryUsedBytes,
		&sample.RootDiskTotalBytes, &sample.RootDiskUsedBytes, &sample.Load1, &sample.Load5, &sample.Load15,
		&sample.NetworkRXTotalBytes, &sample.NetworkTXTotalBytes, &sample.NetworkRXBytesPerSecond,
		&sample.NetworkTXBytesPerSecond, &sample.UptimeSeconds, &sample.System.Hostname, &sample.System.OS,
		&sample.System.Kernel, &sample.System.Arch,
	)
	return sample, err
}

func validateMinuteSample(sample domain.MinuteSample) error {
	if sample.ServerID <= 0 {
		return errors.New("server ID must be positive")
	}
	if sample.BucketMS <= 0 || sample.BucketMS%60_000 != 0 {
		return errors.New("bucket must be the start of a UTC minute")
	}
	floats := []float64{
		sample.CPUPct, sample.Load1, sample.Load5, sample.Load15,
		sample.NetworkRXBytesPerSecond, sample.NetworkTXBytesPerSecond,
	}
	for _, value := range floats {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("numeric fields must be finite")
		}
	}
	if sample.CPUPct < 0 || sample.CPUPct > 100 {
		return errors.New("cpu_pct must be between 0 and 100")
	}
	if sample.MemoryTotalBytes <= 0 || sample.MemoryUsedBytes < 0 || sample.MemoryUsedBytes > sample.MemoryTotalBytes {
		return errors.New("memory values are invalid")
	}
	if sample.RootDiskTotalBytes <= 0 || sample.RootDiskUsedBytes < 0 || sample.RootDiskUsedBytes > sample.RootDiskTotalBytes {
		return errors.New("root disk values are invalid")
	}
	if sample.Load1 < 0 || sample.Load5 < 0 || sample.Load15 < 0 || sample.NetworkRXBytesPerSecond < 0 || sample.NetworkTXBytesPerSecond < 0 {
		return errors.New("loads and network rates must be non-negative")
	}
	if sample.NetworkRXTotalBytes < 0 || sample.NetworkTXTotalBytes < 0 || sample.UptimeSeconds < 0 {
		return errors.New("counters and uptime must be non-negative")
	}
	if strings.TrimSpace(sample.System.Hostname) == "" || strings.TrimSpace(sample.System.OS) == "" || strings.TrimSpace(sample.System.Kernel) == "" || strings.TrimSpace(sample.System.Arch) == "" {
		return errors.New("system fields must be non-empty")
	}
	return nil
}
