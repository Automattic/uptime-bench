package dockerstats

import "testing"

func TestSampleFromStats(t *testing.T) {
	container := dockerContainer{
		ID:    "1234567890abcdef",
		Names: []string{"/jetmon-1"},
		Image: "jetmon:test",
		Labels: map[string]string{
			"com.docker.compose.project": "jetmon",
			"com.docker.compose.service": "app",
		},
	}
	var stats dockerStats
	stats.CPUStats.CPUUsage.TotalUsage = 3_000_000_000
	stats.CPUStats.CPUUsage.PercpuUsage = []uint64{1, 1}
	stats.CPUStats.SystemCPUUsage = 6_000_000_000
	stats.PreCPUStats.CPUUsage.TotalUsage = 1_000_000_000
	stats.PreCPUStats.SystemCPUUsage = 2_000_000_000
	stats.MemoryStats.Usage = 1024
	stats.MemoryStats.Limit = 4096
	stats.MemoryStats.Stats = map[string]uint64{"inactive_file": 256}
	stats.Networks = map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	}{
		"eth0": {RxBytes: 100, TxBytes: 50},
		"eth1": {RxBytes: 7, TxBytes: 3},
	}
	stats.BlkioStats.IoServiceBytesRecursive = []blkioStat{
		{Op: "Read", Value: 4096},
		{Op: "Write", Value: 8192},
		{Op: "Sync", Value: 123},
		{Op: "Total", Value: 12411},
	}
	stats.BlkioStats.IoServicedRecursive = []blkioStat{
		{Op: "read", Value: 3},
		{Op: "write", Value: 5},
		{Op: "total", Value: 8},
	}
	stats.PIDsStats.Current = 8

	sample := sampleFromStats(container, stats)
	if sample.Container.Name != "jetmon-1" {
		t.Fatalf("name = %q, want jetmon-1", sample.Container.Name)
	}
	if sample.CPUUsageSeconds != 3 {
		t.Fatalf("CPUUsageSeconds = %v, want 3", sample.CPUUsageSeconds)
	}
	if sample.CPUPercent != 100 {
		t.Fatalf("CPUPercent = %v, want 100", sample.CPUPercent)
	}
	if sample.MemoryWorkingSetBytes != 768 {
		t.Fatalf("MemoryWorkingSetBytes = %v, want 768", sample.MemoryWorkingSetBytes)
	}
	if sample.NetworkReceiveBytes != 107 || sample.NetworkTransmitBytes != 53 {
		t.Fatalf("network = %v/%v, want 107/53", sample.NetworkReceiveBytes, sample.NetworkTransmitBytes)
	}
	if sample.BlockReadBytes != 4096 || sample.BlockWriteBytes != 8192 {
		t.Fatalf("block bytes = %v/%v, want 4096/8192", sample.BlockReadBytes, sample.BlockWriteBytes)
	}
	if sample.BlockReadOps != 3 || sample.BlockWriteOps != 5 {
		t.Fatalf("block ops = %v/%v, want 3/5", sample.BlockReadOps, sample.BlockWriteOps)
	}
	if sample.PIDs != 8 {
		t.Fatalf("PIDs = %v, want 8", sample.PIDs)
	}
}

func TestCPUPercentHandlesMissingPreCPU(t *testing.T) {
	var stats dockerStats
	stats.CPUStats.CPUUsage.TotalUsage = 1
	stats.CPUStats.SystemCPUUsage = 1

	if got := cpuPercent(stats); got != 0 {
		t.Fatalf("cpuPercent = %v, want 0", got)
	}
}
