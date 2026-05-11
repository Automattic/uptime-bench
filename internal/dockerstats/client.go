package dockerstats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// Client reads container inventory and resource stats from a local Docker socket.
type Client struct {
	SocketPath string
	HTTPClient *http.Client
	// MaxParallel limits concurrent Docker stats calls. Zero means 8.
	MaxParallel int
}

// Container is the stable Docker metadata attached to every stats sample.
type Container struct {
	ID     string
	Name   string
	Image  string
	Labels map[string]string
}

// Sample is one point-in-time Docker container stats sample.
type Sample struct {
	Container             Container
	CPUUsageSeconds       float64
	CPUPercent            float64
	MemoryUsageBytes      float64
	MemoryWorkingSetBytes float64
	MemoryLimitBytes      float64
	NetworkReceiveBytes   float64
	NetworkTransmitBytes  float64
	BlockReadBytes        float64
	BlockWriteBytes       float64
	BlockReadOps          float64
	BlockWriteOps         float64
	PIDs                  float64
}

type dockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
}

type dockerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IoServiceBytesRecursive []blkioStat `json:"io_service_bytes_recursive"`
		IoServicedRecursive     []blkioStat `json:"io_serviced_recursive"`
	} `json:"blkio_stats"`
	PIDsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

type blkioStat struct {
	Major uint64 `json:"major"`
	Minor uint64 `json:"minor"`
	Op    string `json:"op"`
	Value uint64 `json:"value"`
}

// Collect returns stats for all running Docker containers.
func (c *Client) Collect(ctx context.Context) ([]Sample, error) {
	containers, err := c.containers(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Sample, len(containers))
	parallel := c.MaxParallel
	if parallel <= 0 {
		parallel = 8
	}
	sem := make(chan struct{}, parallel)
	errs := make(chan error, len(containers))
	var wg sync.WaitGroup
	for i, container := range containers {
		i, container := i, container
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
			stats, err := c.stats(ctx, container.ID)
			if err != nil {
				errs <- fmt.Errorf("container %s: %w", containerName(container), err)
				return
			}
			out[i] = sampleFromStats(container, stats)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Client) containers(ctx context.Context) ([]dockerContainer, error) {
	var containers []dockerContainer
	if err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"false"}}, &containers); err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}
	return containers, nil
}

func (c *Client) stats(ctx context.Context, id string) (dockerStats, error) {
	var stats dockerStats
	endpoint := path.Join("/containers", id, "stats")
	if err := c.getJSON(ctx, endpoint, url.Values{"stream": {"false"}}, &stats); err != nil {
		return dockerStats{}, fmt.Errorf("docker: stats: %w", err)
	}
	return stats, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, values url.Values, out any) error {
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	u := url.URL{
		Scheme:   "http",
		Host:     "docker",
		Path:     endpoint,
		RawQuery: values.Encode(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}

	client := c.HTTPClient
	if client == nil {
		client = unixHTTPClient(c.socketPath(), 15*time.Second)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	return nil
}

func (c *Client) socketPath() string {
	if c.SocketPath != "" {
		return c.SocketPath
	}
	return "/var/run/docker.sock"
}

func unixHTTPClient(socketPath string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableCompression: true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}

func sampleFromStats(container dockerContainer, stats dockerStats) Sample {
	rx, tx := networkTotals(stats)
	blockReadBytes, blockWriteBytes, blockReadOps, blockWriteOps := blockIOTotals(stats)
	usage := float64(stats.MemoryStats.Usage)
	inactiveFile := stats.MemoryStats.Stats["inactive_file"]
	if inactiveFile == 0 {
		inactiveFile = stats.MemoryStats.Stats["total_inactive_file"]
	}
	workingSet := stats.MemoryStats.Usage
	if inactiveFile < workingSet {
		workingSet -= inactiveFile
	}
	return Sample{
		Container: Container{
			ID:     container.ID,
			Name:   containerName(container),
			Image:  container.Image,
			Labels: container.Labels,
		},
		CPUUsageSeconds:       float64(stats.CPUStats.CPUUsage.TotalUsage) / float64(time.Second),
		CPUPercent:            cpuPercent(stats),
		MemoryUsageBytes:      usage,
		MemoryWorkingSetBytes: float64(workingSet),
		MemoryLimitBytes:      float64(stats.MemoryStats.Limit),
		NetworkReceiveBytes:   float64(rx),
		NetworkTransmitBytes:  float64(tx),
		BlockReadBytes:        float64(blockReadBytes),
		BlockWriteBytes:       float64(blockWriteBytes),
		BlockReadOps:          float64(blockReadOps),
		BlockWriteOps:         float64(blockWriteOps),
		PIDs:                  float64(stats.PIDsStats.Current),
	}
}

func containerName(container dockerContainer) string {
	for _, name := range container.Names {
		name = strings.TrimPrefix(name, "/")
		if name != "" {
			return name
		}
	}
	if len(container.ID) > 12 {
		return container.ID[:12]
	}
	return container.ID
}

func cpuPercent(stats dockerStats) float64 {
	if stats.PreCPUStats.CPUUsage.TotalUsage == 0 || stats.PreCPUStats.SystemCPUUsage == 0 {
		return 0
	}
	if stats.CPUStats.CPUUsage.TotalUsage < stats.PreCPUStats.CPUUsage.TotalUsage ||
		stats.CPUStats.SystemCPUUsage < stats.PreCPUStats.SystemCPUUsage {
		return 0
	}
	cpuDelta := stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage
	systemDelta := stats.CPUStats.SystemCPUUsage - stats.PreCPUStats.SystemCPUUsage
	if cpuDelta == 0 || systemDelta == 0 {
		return 0
	}
	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}
	return (float64(cpuDelta) / float64(systemDelta)) * onlineCPUs * 100
}

func networkTotals(stats dockerStats) (uint64, uint64) {
	var rx, tx uint64
	for _, network := range stats.Networks {
		rx += network.RxBytes
		tx += network.TxBytes
	}
	return rx, tx
}

func blockIOTotals(stats dockerStats) (readBytes, writeBytes, readOps, writeOps uint64) {
	for _, entry := range stats.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(entry.Op) {
		case "read":
			readBytes += entry.Value
		case "write":
			writeBytes += entry.Value
		}
	}
	for _, entry := range stats.BlkioStats.IoServicedRecursive {
		switch strings.ToLower(entry.Op) {
		case "read":
			readOps += entry.Value
		case "write":
			writeOps += entry.Value
		}
	}
	return readBytes, writeBytes, readOps, writeOps
}
