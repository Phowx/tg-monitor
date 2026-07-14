package linuxmetrics

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

func parseCPU(reader io.Reader) (CPUCounters, error) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		if len(fields) < 5 {
			return CPUCounters{}, errors.New("parse /proc/stat: aggregate CPU line has too few fields")
		}
		values := make([]uint64, 0, len(fields)-1)
		var total uint64
		for index, raw := range fields[1:] {
			value, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return CPUCounters{}, errors.New("parse /proc/stat: invalid CPU counter")
			}
			values = append(values, value)
			if index >= 8 {
				continue
			}
			if math.MaxUint64-total < value {
				return CPUCounters{}, errors.New("parse /proc/stat: CPU counter sum overflows")
			}
			total += value
		}
		idle := values[3]
		if len(values) > 4 {
			if math.MaxUint64-idle < values[4] {
				return CPUCounters{}, errors.New("parse /proc/stat: idle counter sum overflows")
			}
			idle += values[4]
		}
		return CPUCounters{Total: total, Idle: idle}, nil
	}
	if err := scanner.Err(); err != nil {
		return CPUCounters{}, fmt.Errorf("parse /proc/stat: %w", err)
	}
	return CPUCounters{}, errors.New("parse /proc/stat: aggregate CPU line is missing")
}

func parseMemory(reader io.Reader) (int64, int64, error) {
	requiredKeys := map[string]struct{}{
		"MemTotal": {}, "MemAvailable": {}, "MemFree": {}, "Buffers": {},
		"Cached": {}, "SReclaimable": {}, "Shmem": {},
	}
	values := make(map[string]uint64, len(requiredKeys))
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if _, wanted := requiredKeys[key]; !wanted {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return 0, 0, fmt.Errorf("parse /proc/meminfo: %s must use kB", key)
		}
		if _, duplicate := values[key]; duplicate {
			return 0, 0, fmt.Errorf("parse /proc/meminfo: duplicate %s", key)
		}
		kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kilobytes > uint64(math.MaxInt64/1024) {
			return 0, 0, fmt.Errorf("parse /proc/meminfo: invalid %s", key)
		}
		values[key] = kilobytes * 1024
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("parse /proc/meminfo: %w", err)
	}

	total, ok := values["MemTotal"]
	if !ok || total == 0 {
		return 0, 0, errors.New("parse /proc/meminfo: MemTotal is missing or zero")
	}
	available, hasAvailable := values["MemAvailable"]
	if !hasAvailable {
		var fallbackOK bool
		available, fallbackOK = memoryFallback(values)
		if !fallbackOK {
			return 0, 0, errors.New("parse /proc/meminfo: MemAvailable and fallback fields are missing")
		}
	}
	if available > total {
		available = total
	}
	return int64(total), int64(total - available), nil
}

func memoryFallback(values map[string]uint64) (uint64, bool) {
	free, freeOK := values["MemFree"]
	buffers, buffersOK := values["Buffers"]
	cached, cachedOK := values["Cached"]
	if !freeOK || !buffersOK || !cachedOK {
		return 0, false
	}
	available, ok := checkedAdd(free, buffers)
	if !ok {
		return 0, false
	}
	available, ok = checkedAdd(available, cached)
	if !ok {
		return 0, false
	}
	available, ok = checkedAdd(available, values["SReclaimable"])
	if !ok {
		return 0, false
	}
	shmem := values["Shmem"]
	if shmem >= available {
		return 0, true
	}
	return available - shmem, true
}

func parseLoad(reader io.Reader) (float64, float64, float64, error) {
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return 0, 0, 0, fmt.Errorf("parse /proc/loadavg: %w", err)
		}
		return 0, 0, 0, errors.New("parse /proc/loadavg: input is empty")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("parse /proc/loadavg: fewer than three load values")
	}
	values := [3]float64{}
	for index := range values {
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, 0, 0, errors.New("parse /proc/loadavg: invalid load value")
		}
		values[index] = value
	}
	return values[0], values[1], values[2], nil
}

func parseNetwork(reader io.Reader) (NetworkCounters, error) {
	scanner := bufio.NewScanner(reader)
	var result NetworkCounters
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.Contains(line, "|") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			return NetworkCounters{}, errors.New("parse /proc/net/dev: interface line is missing colon")
		}
		name := strings.TrimSpace(line[:colon])
		fields := strings.Fields(line[colon+1:])
		if name == "" || len(fields) < 16 {
			return NetworkCounters{}, errors.New("parse /proc/net/dev: malformed interface line")
		}
		if name == "lo" {
			continue
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return NetworkCounters{}, errors.New("parse /proc/net/dev: invalid receive bytes")
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return NetworkCounters{}, errors.New("parse /proc/net/dev: invalid transmit bytes")
		}
		var ok bool
		result.RXBytes, ok = checkedAdd(result.RXBytes, rx)
		if !ok {
			return NetworkCounters{}, errors.New("parse /proc/net/dev: receive byte sum overflows")
		}
		result.TXBytes, ok = checkedAdd(result.TXBytes, tx)
		if !ok {
			return NetworkCounters{}, errors.New("parse /proc/net/dev: transmit byte sum overflows")
		}
	}
	if err := scanner.Err(); err != nil {
		return NetworkCounters{}, fmt.Errorf("parse /proc/net/dev: %w", err)
	}
	return result, nil
}

func parseUptime(reader io.Reader) (int64, error) {
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return 0, fmt.Errorf("parse /proc/uptime: %w", err)
		}
		return 0, errors.New("parse /proc/uptime: input is empty")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) == 0 {
		return 0, errors.New("parse /proc/uptime: uptime is missing")
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value >= float64(math.MaxInt64) {
		return 0, errors.New("parse /proc/uptime: invalid uptime")
	}
	return int64(math.Floor(value)), nil
}

func checkedAdd(left, right uint64) (uint64, bool) {
	if math.MaxUint64-left < right {
		return 0, false
	}
	return left + right, true
}
