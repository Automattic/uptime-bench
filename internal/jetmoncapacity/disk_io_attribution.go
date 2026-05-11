package jetmoncapacity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
)

const diskIOAttributionMismatchRatio = 10
const diskIOAttributionMismatchFloor = 1024 * 1024

// DiskIOAttributionCollector captures host-level evidence that explains disk
// I/O seen in Prometheus summaries.
type DiskIOAttributionCollector interface {
	Start(ctx context.Context, cfg RunConfig, services []ServiceLifecycle, start time.Time, duration time.Duration) (DiskIOAttributionHandle, error)
}

// DiskIOAttributionHandle is a live attribution capture that spans the capacity
// window.
type DiskIOAttributionHandle interface {
	Finish(ctx context.Context, end time.Time) (DiskIOAttributionRun, error)
	Cancel()
}

// DefaultDiskIOAttributionCollector captures process I/O snapshots and starts
// pidstat/iostat commands over SSH.
type DefaultDiskIOAttributionCollector struct{}

// DiskIOAttributionRun is the reportable disk attribution artifact for one
// capacity window.
type DiskIOAttributionRun struct {
	Status          string                     `json:"status"`
	Error           string                     `json:"error,omitempty"`
	CapturedAt      time.Time                  `json:"captured_at"`
	Start           time.Time                  `json:"start"`
	End             time.Time                  `json:"end"`
	DurationSeconds float64                    `json:"duration_seconds"`
	Hosts           []DiskIOAttributionHost    `json:"hosts,omitempty"`
	Summaries       []DiskIOAttributionSummary `json:"summaries,omitempty"`
	Warnings        []string                   `json:"warnings,omitempty"`
}

// DiskIOAttributionHost contains host-local attribution evidence.
type DiskIOAttributionHost struct {
	ID                string                    `json:"id"`
	Instance          string                    `json:"instance,omitempty"`
	SSHHost           string                    `json:"ssh_host"`
	Status            string                    `json:"status"`
	Error             string                    `json:"error,omitempty"`
	CapturedAt        time.Time                 `json:"captured_at"`
	ProcessStart      []ProcessIOSnapshot       `json:"process_io_start,omitempty"`
	ProcessEnd        []ProcessIOSnapshot       `json:"process_io_end,omitempty"`
	ProcessDeltas     []ProcessIODelta          `json:"process_io_deltas,omitempty"`
	TopReadProcesses  []ProcessIODelta          `json:"top_read_processes,omitempty"`
	TopWriteProcesses []ProcessIODelta          `json:"top_write_processes,omitempty"`
	DockerContainers  []DockerProcess           `json:"docker_containers,omitempty"`
	Mounts            []MountInfo               `json:"mounts,omitempty"`
	DeviceIO          []DeviceIOSummary         `json:"device_io,omitempty"`
	PidstatStatus     string                    `json:"pidstat_status,omitempty"`
	PidstatError      string                    `json:"pidstat_error,omitempty"`
	PidstatOutput     string                    `json:"-"`
	IostatStatus      string                    `json:"iostat_status,omitempty"`
	IostatError       string                    `json:"iostat_error,omitempty"`
	IostatOutput      string                    `json:"-"`
	Summary           *DiskIOAttributionSummary `json:"summary,omitempty"`
	Warnings          []string                  `json:"warnings,omitempty"`
}

// ProcessIOSnapshot mirrors the useful fields from /proc/<pid>/io.
type ProcessIOSnapshot struct {
	PID            int               `json:"pid"`
	StartTimeTicks uint64            `json:"start_time_ticks,omitempty"`
	Comm           string            `json:"comm,omitempty"`
	Cmdline        string            `json:"cmdline,omitempty"`
	Cgroup         string            `json:"cgroup,omitempty"`
	Label          string            `json:"label,omitempty"`
	ContainerID    string            `json:"container_id,omitempty"`
	ContainerName  string            `json:"container_name,omitempty"`
	Counters       ProcessIOCounters `json:"counters"`
}

// ProcessIOCounters are monotonically increasing /proc/<pid>/io counters.
type ProcessIOCounters struct {
	RChar               uint64 `json:"rchar"`
	WChar               uint64 `json:"wchar"`
	SyscR               uint64 `json:"syscr"`
	SyscW               uint64 `json:"syscw"`
	ReadBytes           uint64 `json:"read_bytes"`
	WriteBytes          uint64 `json:"write_bytes"`
	CancelledWriteBytes uint64 `json:"cancelled_write_bytes"`
}

// ProcessIODelta is a process-level counter delta over the capacity window.
type ProcessIODelta struct {
	PID                 int               `json:"pid"`
	StartTimeTicks      uint64            `json:"start_time_ticks,omitempty"`
	Comm                string            `json:"comm,omitempty"`
	Cmdline             string            `json:"cmdline,omitempty"`
	Label               string            `json:"label,omitempty"`
	ContainerID         string            `json:"container_id,omitempty"`
	ContainerName       string            `json:"container_name,omitempty"`
	Delta               ProcessIOCounters `json:"delta"`
	ReadBytesPerSecond  float64           `json:"read_bytes_per_second"`
	WriteBytesPerSecond float64           `json:"write_bytes_per_second"`
	RCharPerSecond      float64           `json:"rchar_per_second"`
	WCharPerSecond      float64           `json:"wchar_per_second"`
}

// DockerProcess maps a Docker container to its host PID.
type DockerProcess struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	HostPID int    `json:"host_pid"`
}

// MountInfo records filesystem context for paths that commonly explain host
// disk I/O.
type MountInfo struct {
	Path    string `json:"path"`
	Target  string `json:"target"`
	Source  string `json:"source"`
	FSType  string `json:"fstype"`
	Options string `json:"options,omitempty"`
}

// DeviceIOSummary is an averaged iostat -xz view for one block device.
type DeviceIOSummary struct {
	Device                  string  `json:"device"`
	Samples                 int     `json:"samples"`
	ReadOpsPerSecond        float64 `json:"read_ops_per_second,omitempty"`
	WriteOpsPerSecond       float64 `json:"write_ops_per_second,omitempty"`
	ReadKiBPerSecond        float64 `json:"read_kib_per_second,omitempty"`
	WriteKiBPerSecond       float64 `json:"write_kib_per_second,omitempty"`
	AverageWaitMilliseconds float64 `json:"average_wait_milliseconds,omitempty"`
	UtilPercent             float64 `json:"util_percent,omitempty"`
}

// DiskIOAttributionSummary compares Prometheus host/container rates with
// process-level attribution from /proc.
type DiskIOAttributionSummary struct {
	ID                            string   `json:"id"`
	Instance                      string   `json:"instance,omitempty"`
	Status                        string   `json:"status"`
	HostReadBytesPerSecond        float64  `json:"host_read_bytes_per_second,omitempty"`
	HostWriteBytesPerSecond       float64  `json:"host_write_bytes_per_second,omitempty"`
	ProcessReadBytesPerSecond     float64  `json:"process_read_bytes_per_second,omitempty"`
	ProcessWriteBytesPerSecond    float64  `json:"process_write_bytes_per_second,omitempty"`
	ContainerReadBytesPerSecond   float64  `json:"container_read_bytes_per_second,omitempty"`
	ContainerWriteBytesPerSecond  float64  `json:"container_write_bytes_per_second,omitempty"`
	AttributedReadBytesPerSecond  float64  `json:"attributed_read_bytes_per_second,omitempty"`
	AttributedWriteBytesPerSecond float64  `json:"attributed_write_bytes_per_second,omitempty"`
	ReadAttributionRatio          float64  `json:"read_attribution_ratio,omitempty"`
	WriteAttributionRatio         float64  `json:"write_attribution_ratio,omitempty"`
	TopReadProcess                string   `json:"top_read_process,omitempty"`
	TopWriteProcess               string   `json:"top_write_process,omitempty"`
	TopDevice                     string   `json:"top_device,omitempty"`
	Warnings                      []string `json:"warnings,omitempty"`
}

type diskIOCaptureHandle struct {
	cfg      DiskIOAttributionConfig
	hosts    []diskIOHostCapture
	start    time.Time
	duration time.Duration
	cancel   context.CancelFunc
}

type diskIOHostCapture struct {
	host       DiskIOAttributionHostConfig
	start      []ProcessIOSnapshot
	containers []DockerProcess
	mounts     []MountInfo
	status     string
	err        string
	pidstat    *diskIOSampleCommand
	iostat     *diskIOSampleCommand
}

type diskIOSampleCommand struct {
	tool   string
	cmd    *exec.Cmd
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func defaultDiskIOProcessPatterns() []string {
	return []string{
		"jetmon2",
		"jetmon",
		"mysqld",
		"mariadbd",
		"statsd",
		"carbon-cache",
		"graphite",
		"prometheus",
		"node_exporter",
		"process-exporter",
		"cadvisor",
		"uptime-bench-dockerstats-exporter",
		"dockerd",
		"containerd",
		"systemd-journald",
	}
}

func defaultDiskIOMountPaths() []string {
	return []string{
		"/",
		"/var/lib/docker",
		"/var/lib/mysql",
		"/var/log",
		"/var/lib/graphite",
		"/opt/graphite",
		"/home/jetmon",
		"/var/lib/jetmon",
	}
}

func (DefaultDiskIOAttributionCollector) Start(ctx context.Context, cfg RunConfig, services []ServiceLifecycle, start time.Time, duration time.Duration) (DiskIOAttributionHandle, error) {
	cfg = cfg.Normalize()
	if !cfg.DiskIOAttribution.Enabled {
		return nil, nil
	}
	timeout, err := cfg.DiskIOAttributionTimeout()
	if err != nil {
		return nil, err
	}
	interval, err := cfg.DiskIOAttributionSampleInterval()
	if err != nil {
		return nil, err
	}
	hosts := selectedDiskIOAttributionHosts(cfg, services)
	if len(hosts) == 0 {
		return nil, fmt.Errorf("disk I/O attribution enabled but no selected hosts are configured")
	}
	commandCtx, cancel := context.WithCancel(ctx)
	handle := &diskIOCaptureHandle{
		cfg:      cfg.DiskIOAttribution,
		start:    start.UTC(),
		duration: duration,
		cancel:   cancel,
	}
	var errs []error
	for _, host := range hosts {
		capture := diskIOHostCapture{
			host:   host,
			status: "pass",
		}
		containers, err := captureDockerProcesses(ctx, cfg.DiskIOAttribution, host, timeout)
		if err != nil {
			capture.status = "partial"
			capture.err = appendReason(capture.err, "docker inspect: "+err.Error())
			errs = append(errs, fmt.Errorf("%s docker inspect: %w", host.ID, err))
		}
		capture.containers = containers
		processes, err := captureProcessIOSnapshot(ctx, cfg.DiskIOAttribution, host, containers, timeout)
		if err != nil {
			capture.status = "partial"
			capture.err = appendReason(capture.err, "process I/O start: "+err.Error())
			errs = append(errs, fmt.Errorf("%s process I/O start: %w", host.ID, err))
		}
		capture.start = processes
		mounts, err := captureMountInfo(ctx, cfg.DiskIOAttribution, host, timeout)
		if err != nil {
			capture.status = "partial"
			capture.err = appendReason(capture.err, "mounts: "+err.Error())
			errs = append(errs, fmt.Errorf("%s mounts: %w", host.ID, err))
		}
		capture.mounts = mounts
		capture.pidstat = startDiskIOSampleCommand(commandCtx, cfg.DiskIOAttribution, host, "pidstat", interval, duration)
		capture.iostat = startDiskIOSampleCommand(commandCtx, cfg.DiskIOAttribution, host, "iostat", interval, duration)
		handle.hosts = append(handle.hosts, capture)
	}
	if len(handle.hosts) == 0 {
		cancel()
		return nil, joinErrors(errs)
	}
	return handle, nil
}

func (h *diskIOCaptureHandle) Finish(ctx context.Context, end time.Time) (DiskIOAttributionRun, error) {
	if h == nil {
		return DiskIOAttributionRun{Status: "disabled"}, nil
	}
	timeout, err := parseDuration("disk_io_attribution.timeout", h.cfg.Timeout)
	if err != nil {
		return DiskIOAttributionRun{}, err
	}
	run := DiskIOAttributionRun{
		Status:          "pass",
		CapturedAt:      time.Now().UTC(),
		Start:           h.start.UTC(),
		End:             end.UTC(),
		DurationSeconds: end.Sub(h.start).Seconds(),
	}
	if run.DurationSeconds <= 0 {
		run.DurationSeconds = h.duration.Seconds()
	}
	var errs []error
	for _, capture := range h.hosts {
		host := DiskIOAttributionHost{
			ID:               capture.host.ID,
			Instance:         capture.host.Instance,
			SSHHost:          capture.host.SSHHost,
			Status:           firstNonEmpty(capture.status, "pass"),
			Error:            capture.err,
			CapturedAt:       time.Now().UTC(),
			ProcessStart:     capture.start,
			DockerContainers: capture.containers,
			Mounts:           capture.mounts,
		}
		endSnapshot, err := captureProcessIOSnapshot(ctx, h.cfg, capture.host, capture.containers, timeout)
		if err != nil {
			host.Status = "partial"
			host.Error = appendReason(host.Error, "process I/O end: "+err.Error())
			errs = append(errs, fmt.Errorf("%s process I/O end: %w", capture.host.ID, err))
		}
		host.ProcessEnd = endSnapshot
		host.ProcessDeltas = processIODeltas(capture.start, endSnapshot, time.Duration(run.DurationSeconds*float64(time.Second)))
		host.TopReadProcesses = topProcessIODeltas(host.ProcessDeltas, "read_bytes", 10)
		host.TopWriteProcesses = topProcessIODeltas(host.ProcessDeltas, "write_bytes", 10)
		host.PidstatStatus, host.PidstatOutput, host.PidstatError = finishDiskIOSampleCommand(capture.pidstat, timeout)
		host.IostatStatus, host.IostatOutput, host.IostatError = finishDiskIOSampleCommand(capture.iostat, timeout)
		if host.IostatStatus == "pass" {
			host.DeviceIO = parseIostatDeviceSummaries(host.IostatOutput)
		}
		if host.PidstatStatus == "fail" {
			host.Status = "partial"
			host.Error = appendReason(host.Error, "pidstat: "+host.PidstatError)
		}
		if host.IostatStatus == "fail" {
			host.Status = "partial"
			host.Error = appendReason(host.Error, "iostat: "+host.IostatError)
		}
		if host.Status == "partial" {
			run.Status = "partial"
		}
		run.Hosts = append(run.Hosts, host)
	}
	if run.Status == "partial" {
		if joined := joinErrors(errs); joined != nil {
			run.Error = joined.Error()
		}
	}
	return run, joinErrors(errs)
}

func (h *diskIOCaptureHandle) Cancel() {
	if h != nil && h.cancel != nil {
		h.cancel()
	}
}

func (r Runner) startDiskIOAttribution(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, start time.Time, duration time.Duration, m *RunManifest) (DiskIOAttributionHandle, error) {
	if !cfg.Normalize().DiskIOAttribution.Enabled {
		return nil, nil
	}
	handle, err := r.DiskIOAttribution.Start(ctx, cfg, services, start, duration)
	if err != nil {
		return nil, err
	}
	if handle != nil {
		m.DiskIOAttributionStatus = "running"
		m.DiskIOAttributionError = ""
	}
	return handle, nil
}

func (r Runner) finishDiskIOAttribution(ctx context.Context, dir string, handle DiskIOAttributionHandle, end time.Time, m *RunManifest) error {
	if handle == nil {
		return nil
	}
	run, err := handle.Finish(ctx, end)
	m.DiskIOAttribution = append(m.DiskIOAttribution, run)
	if writeErr := writeDiskIOAttributionArtifacts(dir, run, m); writeErr != nil {
		if err == nil {
			err = writeErr
		} else {
			err = errors.Join(err, writeErr)
		}
	}
	if err != nil {
		m.DiskIOAttributionStatus = firstNonEmpty(run.Status, "partial")
		m.DiskIOAttributionError = err.Error()
		return err
	}
	m.DiskIOAttributionStatus = firstNonEmpty(run.Status, "pass")
	m.DiskIOAttributionError = ""
	return nil
}

func (r Runner) annotateDiskIOAttribution(ctx context.Context, dir string, prom *capacitybench.Report, m *RunManifest) error {
	if prom == nil || len(m.DiskIOAttribution) == 0 {
		return nil
	}
	latest := &m.DiskIOAttribution[len(m.DiskIOAttribution)-1]
	annotateDiskIOAttribution(latest, prom)
	if latest.Status != "" {
		m.DiskIOAttributionStatus = latest.Status
	}
	if len(latest.Warnings) > 0 {
		for _, warning := range latest.Warnings {
			m.Notes = append(m.Notes, "Disk I/O attribution warning: "+warning)
		}
	}
	return writeDiskIOAttributionArtifacts(dir, *latest, m)
}

func selectedDiskIOAttributionHosts(cfg RunConfig, services []ServiceLifecycle) []DiskIOAttributionHostConfig {
	disk := cfg.DiskIOAttribution
	var hosts []DiskIOAttributionHostConfig
	if len(disk.Hosts) > 0 {
		hosts = append(hosts, disk.Hosts...)
	} else {
		for _, host := range cfg.NetworkBuckets.Hosts {
			hosts = append(hosts, normalizeDiskIOAttributionHost(disk, DiskIOAttributionHostConfig{
				ID:       host.ID,
				Instance: host.Instance,
				SSHHost:  host.SSHHost,
			}))
		}
	}
	if len(services) == 0 {
		return hosts
	}
	selected := map[string]bool{}
	for _, service := range services {
		selected[service.ID] = true
	}
	var out []DiskIOAttributionHostConfig
	for _, host := range hosts {
		if selected[host.ID] {
			out = append(out, host)
		}
	}
	return out
}

func captureDockerProcesses(ctx context.Context, cfg DiskIOAttributionConfig, host DiskIOAttributionHostConfig, timeout time.Duration) ([]DockerProcess, error) {
	const script = `set +e
printf 'id\tname\thost_pid\n'
if command -v docker >/dev/null 2>&1; then
	ids=$(docker ps -q 2>/dev/null)
	if [ -n "$ids" ]; then
		docker inspect --format '{{.Id}}	{{.Name}}	{{.State.Pid}}' $ids 2>/dev/null
	fi
fi
`
	out, err := runDiskIOSSH(ctx, cfg, host.SSHHost, timeout, []string{"sudo", "sh", "-s"}, script)
	if err != nil {
		return nil, err
	}
	return parseDockerProcesses(out), nil
}

func captureProcessIOSnapshot(ctx context.Context, cfg DiskIOAttributionConfig, host DiskIOAttributionHostConfig, containers []DockerProcess, timeout time.Duration) ([]ProcessIOSnapshot, error) {
	const script = `set +e
printf 'pid\tstart_time_ticks\tcomm\tcmdline\tcgroup\trchar\twchar\tsyscr\tsyscw\tread_bytes\twrite_bytes\tcancelled_write_bytes\n'
for d in /proc/[0-9]*; do
	pid=${d##*/}
	[ -r "$d/io" ] || continue
	comm=$(cat "$d/comm" 2>/dev/null | tr '\t\n' '  ')
	cmdline=$(tr '\000\t\n' '   ' < "$d/cmdline" 2>/dev/null)
	if [ -z "$cmdline" ]; then
		cmdline="[$comm]"
	fi
	cgroup=$(tr '\n\t' '| ' < "$d/cgroup" 2>/dev/null)
	stat=$(cat "$d/stat" 2>/dev/null)
	rest=${stat##*) }
	set -- $rest
	start_time=${20:-0}
	awk -v pid="$pid" -v start_time="$start_time" -v comm="$comm" -v cmdline="$cmdline" -v cgroup="$cgroup" '
		{ key=$1; sub(":", "", key); value[key]=$2 }
		END {
			printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				pid, start_time, comm, cmdline, cgroup,
				value["rchar"]+0, value["wchar"]+0,
				value["syscr"]+0, value["syscw"]+0,
				value["read_bytes"]+0, value["write_bytes"]+0,
				value["cancelled_write_bytes"]+0
		}
	' "$d/io"
done
`
	out, err := runDiskIOSSH(ctx, cfg, host.SSHHost, timeout, []string{"sudo", "sh", "-s"}, script)
	if err != nil {
		return nil, err
	}
	processes, err := parseProcessIOSnapshots(out)
	if err != nil {
		return nil, err
	}
	labelProcessIOSnapshots(processes, containers, host.ProcessPatterns)
	return processes, nil
}

func captureMountInfo(ctx context.Context, cfg DiskIOAttributionConfig, host DiskIOAttributionHostConfig, timeout time.Duration) ([]MountInfo, error) {
	var b strings.Builder
	fmt.Fprintln(&b, "set +e")
	fmt.Fprintln(&b, "printf 'path\\ttarget\\tsource\\tfstype\\toptions\\n'")
	fmt.Fprintln(&b, "if ! command -v findmnt >/dev/null 2>&1; then exit 0; fi")
	for _, path := range host.MountPaths {
		fmt.Fprintf(&b, "if [ -e %s ]; then findmnt -T %s -no TARGET,SOURCE,FSTYPE,OPTIONS 2>/dev/null | head -n 1 | awk -v p=%s '{ target=$1; source=$2; fstype=$3; $1=\"\"; $2=\"\"; $3=\"\"; sub(/^ +/, \"\"); printf \"%%s\\t%%s\\t%%s\\t%%s\\t%%s\\n\", p, target, source, fstype, $0 }'; fi\n",
			shellQuote(path), shellQuote(path), shellQuote(path))
	}
	out, err := runDiskIOSSH(ctx, cfg, host.SSHHost, timeout, []string{"sudo", "sh", "-s"}, b.String())
	if err != nil {
		return nil, err
	}
	return parseMountInfo(out), nil
}

func startDiskIOSampleCommand(ctx context.Context, cfg DiskIOAttributionConfig, host DiskIOAttributionHostConfig, tool string, interval, duration time.Duration) *diskIOSampleCommand {
	seconds := int(math.Ceil(interval.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	count := int(math.Ceil(duration.Seconds() / float64(seconds)))
	if count < 1 {
		count = 1
	}
	var remote string
	switch tool {
	case "pidstat":
		remote = fmt.Sprintf("if command -v pidstat >/dev/null 2>&1; then pidstat -d -h %d %d; else echo 'pidstat unavailable' >&2; exit 127; fi\n", seconds, count)
	case "iostat":
		remote = fmt.Sprintf("if command -v iostat >/dev/null 2>&1; then iostat -xz %d %d; else echo 'iostat unavailable' >&2; exit 127; fi\n", seconds, count)
	default:
		remote = fmt.Sprintf("echo '%s unsupported' >&2; exit 127\n", tool)
	}
	args := []string{}
	if cfg.SSHConfig != "" {
		args = append(args, "-F", cfg.SSHConfig)
	}
	args = append(args, host.SSHHost, "sh", "-s")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	sample := &diskIOSampleCommand{tool: tool, cmd: cmd}
	cmd.Stdin = strings.NewReader(remote)
	cmd.Stdout = &sample.stdout
	cmd.Stderr = &sample.stderr
	if err := cmd.Start(); err != nil {
		sample.stderr.WriteString(err.Error())
	}
	return sample
}

func finishDiskIOSampleCommand(sample *diskIOSampleCommand, timeout time.Duration) (string, string, string) {
	if sample == nil || sample.cmd == nil || sample.cmd.Process == nil {
		return "fail", "", "command did not start"
	}
	done := make(chan error, 1)
	go func() {
		done <- sample.cmd.Wait()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var err error
	select {
	case err = <-done:
	case <-timer.C:
		_ = sample.cmd.Process.Kill()
		err = fmt.Errorf("timed out waiting for %s", sample.tool)
	}
	output := sample.stdout.String()
	stderr := strings.TrimSpace(sample.stderr.String())
	if err != nil {
		if stderr != "" {
			return "fail", output, err.Error() + ": " + stderr
		}
		return "fail", output, err.Error()
	}
	if stderr != "" {
		return "pass", output, stderr
	}
	return "pass", output, ""
}

func runDiskIOSSH(ctx context.Context, cfg DiskIOAttributionConfig, sshHost string, timeout time.Duration, remoteArgs []string, stdin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{}
	if cfg.SSHConfig != "" {
		args = append(args, "-F", cfg.SSHConfig)
	}
	args = append(args, sshHost)
	args = append(args, remoteArgs...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil {
		return out, fmt.Errorf("%v: %w: %s", append([]string{"ssh"}, args...), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func parseDockerProcesses(data []byte) []DockerProcess {
	var out []DockerProcess
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(cols[2]))
		name := strings.TrimPrefix(strings.TrimSpace(cols[1]), "/")
		out = append(out, DockerProcess{
			ID:      strings.TrimSpace(cols[0]),
			Name:    name,
			HostPID: pid,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func parseProcessIOSnapshots(data []byte) ([]ProcessIOSnapshot, error) {
	var out []ProcessIOSnapshot
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 12 {
			return nil, fmt.Errorf("process I/O line has %d columns, want 12: %q", len(cols), line)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(cols[0]))
		if err != nil {
			continue
		}
		startTime, _ := strconv.ParseUint(strings.TrimSpace(cols[1]), 10, 64)
		out = append(out, ProcessIOSnapshot{
			PID:            pid,
			StartTimeTicks: startTime,
			Comm:           strings.TrimSpace(cols[2]),
			Cmdline:        strings.TrimSpace(cols[3]),
			Cgroup:         strings.TrimSpace(cols[4]),
			Counters: ProcessIOCounters{
				RChar:               parseUintColumn(cols[5]),
				WChar:               parseUintColumn(cols[6]),
				SyscR:               parseUintColumn(cols[7]),
				SyscW:               parseUintColumn(cols[8]),
				ReadBytes:           parseUintColumn(cols[9]),
				WriteBytes:          parseUintColumn(cols[10]),
				CancelledWriteBytes: parseUintColumn(cols[11]),
			},
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PID != out[j].PID {
			return out[i].PID < out[j].PID
		}
		return out[i].StartTimeTicks < out[j].StartTimeTicks
	})
	return out, nil
}

func parseMountInfo(data []byte) []MountInfo {
	var out []MountInfo
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 5 {
			continue
		}
		out = append(out, MountInfo{
			Path:    strings.TrimSpace(cols[0]),
			Target:  strings.TrimSpace(cols[1]),
			Source:  strings.TrimSpace(cols[2]),
			FSType:  strings.TrimSpace(cols[3]),
			Options: strings.TrimSpace(cols[4]),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out
}

func parseIostatDeviceSummaries(output string) []DeviceIOSummary {
	type totals struct {
		samples         int
		readOps         float64
		writeOps        float64
		readKiB         float64
		writeKiB        float64
		await           float64
		readAwait       float64
		writeAwait      float64
		readAwaitCount  int
		writeAwaitCount int
		util            float64
	}
	byDevice := map[string]*totals{}
	var header map[string]int
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Linux ") || strings.HasPrefix(line, "avg-cpu:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "Device" {
			header = map[string]int{}
			for i, field := range fields {
				header[field] = i
			}
			continue
		}
		if header == nil || len(fields) <= header["Device"] {
			continue
		}
		device := fields[header["Device"]]
		if ignoredIostatDevice(device) {
			continue
		}
		total := byDevice[device]
		if total == nil {
			total = &totals{}
			byDevice[device] = total
		}
		total.samples++
		total.readOps += iostatFloat(fields, header, "r/s")
		total.writeOps += iostatFloat(fields, header, "w/s")
		total.readKiB += iostatFloat(fields, header, "rkB/s")
		total.writeKiB += iostatFloat(fields, header, "wkB/s")
		if value, ok := iostatFloatOK(fields, header, "await"); ok {
			total.await += value
		}
		if value, ok := iostatFloatOK(fields, header, "r_await"); ok {
			total.readAwait += value
			total.readAwaitCount++
		}
		if value, ok := iostatFloatOK(fields, header, "w_await"); ok {
			total.writeAwait += value
			total.writeAwaitCount++
		}
		total.util += iostatFloat(fields, header, "%util")
	}
	out := make([]DeviceIOSummary, 0, len(byDevice))
	for device, total := range byDevice {
		if total.samples == 0 {
			continue
		}
		await := total.await / float64(total.samples)
		if total.await == 0 && (total.readAwaitCount > 0 || total.writeAwaitCount > 0) {
			var parts int
			var sum float64
			if total.readAwaitCount > 0 {
				sum += total.readAwait / float64(total.readAwaitCount)
				parts++
			}
			if total.writeAwaitCount > 0 {
				sum += total.writeAwait / float64(total.writeAwaitCount)
				parts++
			}
			if parts > 0 {
				await = sum / float64(parts)
			}
		}
		out = append(out, DeviceIOSummary{
			Device:                  device,
			Samples:                 total.samples,
			ReadOpsPerSecond:        total.readOps / float64(total.samples),
			WriteOpsPerSecond:       total.writeOps / float64(total.samples),
			ReadKiBPerSecond:        total.readKiB / float64(total.samples),
			WriteKiBPerSecond:       total.writeKiB / float64(total.samples),
			AverageWaitMilliseconds: await,
			UtilPercent:             total.util / float64(total.samples),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UtilPercent != out[j].UtilPercent {
			return out[i].UtilPercent > out[j].UtilPercent
		}
		left := out[i].ReadKiBPerSecond + out[i].WriteKiBPerSecond
		right := out[j].ReadKiBPerSecond + out[j].WriteKiBPerSecond
		if left != right {
			return left > right
		}
		return out[i].Device < out[j].Device
	})
	return out
}

func iostatFloat(fields []string, header map[string]int, name string) float64 {
	value, _ := iostatFloatOK(fields, header, name)
	return value
}

func iostatFloatOK(fields []string, header map[string]int, name string) (float64, bool) {
	idx, ok := header[name]
	if !ok || idx < 0 || idx >= len(fields) {
		return 0, false
	}
	value, err := strconv.ParseFloat(fields[idx], 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func ignoredIostatDevice(device string) bool {
	return strings.HasPrefix(device, "loop") ||
		strings.HasPrefix(device, "ram") ||
		strings.HasPrefix(device, "zram") ||
		strings.HasPrefix(device, "fd") ||
		strings.HasPrefix(device, "sr")
}

func parseUintColumn(raw string) uint64 {
	value, _ := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	return value
}

func labelProcessIOSnapshots(processes []ProcessIOSnapshot, containers []DockerProcess, patterns []string) {
	for i := range processes {
		if container := matchingContainer(processes[i], containers); container != nil {
			processes[i].ContainerID = container.ID
			processes[i].ContainerName = container.Name
			processes[i].Label = "container:" + container.Name
			continue
		}
		label := matchingProcessPattern(processes[i], patterns)
		if label == "" {
			label = processes[i].Comm
		}
		processes[i].Label = label
	}
}

func matchingContainer(process ProcessIOSnapshot, containers []DockerProcess) *DockerProcess {
	lowerCgroup := strings.ToLower(process.Cgroup)
	for i := range containers {
		if containers[i].HostPID > 0 && process.PID == containers[i].HostPID {
			return &containers[i]
		}
		id := strings.ToLower(containers[i].ID)
		if id != "" && strings.Contains(lowerCgroup, id) {
			return &containers[i]
		}
		if len(id) >= 12 && strings.Contains(lowerCgroup, id[:12]) {
			return &containers[i]
		}
	}
	return nil
}

func matchingProcessPattern(process ProcessIOSnapshot, patterns []string) string {
	haystack := strings.ToLower(process.Comm + " " + process.Cmdline)
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if strings.Contains(haystack, strings.ToLower(pattern)) {
			return pattern
		}
	}
	return ""
}

func processIODeltas(start, end []ProcessIOSnapshot, duration time.Duration) []ProcessIODelta {
	if duration <= 0 {
		duration = time.Second
	}
	seconds := duration.Seconds()
	startByKey := make(map[string]ProcessIOSnapshot, len(start))
	for _, process := range start {
		startByKey[processIOKey(process)] = process
	}
	var out []ProcessIODelta
	for _, after := range end {
		before, ok := startByKey[processIOKey(after)]
		if !ok {
			continue
		}
		delta := ProcessIOCounters{
			RChar:               saturatingSubtract(after.Counters.RChar, before.Counters.RChar),
			WChar:               saturatingSubtract(after.Counters.WChar, before.Counters.WChar),
			SyscR:               saturatingSubtract(after.Counters.SyscR, before.Counters.SyscR),
			SyscW:               saturatingSubtract(after.Counters.SyscW, before.Counters.SyscW),
			ReadBytes:           saturatingSubtract(after.Counters.ReadBytes, before.Counters.ReadBytes),
			WriteBytes:          saturatingSubtract(after.Counters.WriteBytes, before.Counters.WriteBytes),
			CancelledWriteBytes: saturatingSubtract(after.Counters.CancelledWriteBytes, before.Counters.CancelledWriteBytes),
		}
		out = append(out, ProcessIODelta{
			PID:                 after.PID,
			StartTimeTicks:      after.StartTimeTicks,
			Comm:                after.Comm,
			Cmdline:             after.Cmdline,
			Label:               after.Label,
			ContainerID:         after.ContainerID,
			ContainerName:       after.ContainerName,
			Delta:               delta,
			ReadBytesPerSecond:  float64(delta.ReadBytes) / seconds,
			WriteBytesPerSecond: float64(delta.WriteBytes) / seconds,
			RCharPerSecond:      float64(delta.RChar) / seconds,
			WCharPerSecond:      float64(delta.WChar) / seconds,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].PID < out[j].PID
	})
	return out
}

func processIOKey(process ProcessIOSnapshot) string {
	return strconv.Itoa(process.PID) + ":" + strconv.FormatUint(process.StartTimeTicks, 10)
}

func topProcessIODeltas(deltas []ProcessIODelta, metric string, limit int) []ProcessIODelta {
	out := append([]ProcessIODelta(nil), deltas...)
	sort.Slice(out, func(i, j int) bool {
		var a, b uint64
		switch metric {
		case "write_bytes":
			a, b = out[i].Delta.WriteBytes, out[j].Delta.WriteBytes
		default:
			a, b = out[i].Delta.ReadBytes, out[j].Delta.ReadBytes
		}
		if a != b {
			return a > b
		}
		return processDeltaLabel(out[i]) < processDeltaLabel(out[j])
	})
	filtered := out[:0]
	for _, delta := range out {
		var value uint64
		if metric == "write_bytes" {
			value = delta.Delta.WriteBytes
		} else {
			value = delta.Delta.ReadBytes
		}
		if value == 0 {
			continue
		}
		filtered = append(filtered, delta)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return append([]ProcessIODelta(nil), filtered...)
}

func annotateDiskIOAttribution(run *DiskIOAttributionRun, report *capacitybench.Report) {
	if run == nil || report == nil {
		return
	}
	run.Summaries = nil
	run.Warnings = nil
	for i := range run.Hosts {
		host := &run.Hosts[i]
		summary := DiskIOAttributionSummary{
			ID:       host.ID,
			Instance: host.Instance,
			Status:   "partial",
		}
		summary.HostReadBytesPerSecond = prometheusSeriesAvg(report, "host_disk_read_bytes", host.Instance)
		summary.HostWriteBytesPerSecond = prometheusSeriesAvg(report, "host_disk_written_bytes", host.Instance)
		summary.ContainerReadBytesPerSecond = prometheusSeriesSumAvg(report, "docker_container_block_read_bytes", host.Instance)
		summary.ContainerWriteBytesPerSecond = prometheusSeriesSumAvg(report, "docker_container_block_write_bytes", host.Instance)
		for _, delta := range host.ProcessDeltas {
			summary.ProcessReadBytesPerSecond += delta.ReadBytesPerSecond
			summary.ProcessWriteBytesPerSecond += delta.WriteBytesPerSecond
		}
		summary.AttributedReadBytesPerSecond = summary.ProcessReadBytesPerSecond + summary.ContainerReadBytesPerSecond
		summary.AttributedWriteBytesPerSecond = summary.ProcessWriteBytesPerSecond + summary.ContainerWriteBytesPerSecond
		summary.ReadAttributionRatio = attributionRatio(summary.HostReadBytesPerSecond, summary.AttributedReadBytesPerSecond)
		summary.WriteAttributionRatio = attributionRatio(summary.HostWriteBytesPerSecond, summary.AttributedWriteBytesPerSecond)
		if len(host.TopReadProcesses) > 0 {
			summary.TopReadProcess = processDeltaLabel(host.TopReadProcesses[0])
		}
		if len(host.TopWriteProcesses) > 0 {
			summary.TopWriteProcess = processDeltaLabel(host.TopWriteProcesses[0])
		}
		if len(host.DeviceIO) > 0 {
			summary.TopDevice = formatDeviceIOSummary(host.DeviceIO[0])
		}
		summary.Status = "complete"
		if warning := attributionMismatchWarning("read", summary.HostReadBytesPerSecond, summary.AttributedReadBytesPerSecond); warning != "" {
			summary.Status = "mismatch"
			summary.Warnings = append(summary.Warnings, warning)
		}
		if warning := attributionMismatchWarning("write", summary.HostWriteBytesPerSecond, summary.AttributedWriteBytesPerSecond); warning != "" {
			summary.Status = "mismatch"
			summary.Warnings = append(summary.Warnings, warning)
		}
		if summary.HostReadBytesPerSecond == 0 && summary.HostWriteBytesPerSecond == 0 {
			summary.Status = "not_measured"
			summary.Warnings = append(summary.Warnings, "host disk Prometheus series were not available for this instance")
		}
		host.Summary = &summary
		host.Warnings = append(host.Warnings, summary.Warnings...)
		for _, warning := range summary.Warnings {
			run.Warnings = append(run.Warnings, host.ID+": "+warning)
		}
		run.Summaries = append(run.Summaries, summary)
	}
	if len(run.Warnings) > 0 && run.Status == "pass" {
		run.Status = "warning"
	} else if run.Status == "pass" && len(run.Summaries) > 0 {
		run.Status = "complete"
	}
}

func prometheusSeriesAvg(report *capacitybench.Report, query, instance string) float64 {
	for _, summary := range report.Summaries {
		if summary.Query == query && summary.Labels["instance"] == instance {
			return summary.Avg
		}
	}
	return 0
}

func prometheusSeriesSumAvg(report *capacitybench.Report, query, instance string) float64 {
	var total float64
	for _, summary := range report.Summaries {
		if summary.Query == query && summary.Labels["instance"] == instance {
			total += summary.Avg
		}
	}
	return total
}

func attributionRatio(hostRate, attributedRate float64) float64 {
	if hostRate <= 0 {
		return 0
	}
	if attributedRate <= 0 {
		return 0
	}
	return hostRate / attributedRate
}

func attributionMismatchWarning(direction string, hostRate, attributedRate float64) string {
	if hostRate < diskIOAttributionMismatchFloor {
		return ""
	}
	if attributedRate <= 0 {
		return fmt.Sprintf("host %s rate %s had no process/container attribution", direction, capacitybench.FormatValue("bytes_per_second", hostRate))
	}
	ratio := hostRate / attributedRate
	if ratio <= diskIOAttributionMismatchRatio {
		return ""
	}
	return fmt.Sprintf("host %s rate %s was %.1fx attributed process/container rate %s",
		direction,
		capacitybench.FormatValue("bytes_per_second", hostRate),
		ratio,
		capacitybench.FormatValue("bytes_per_second", attributedRate))
}

func processDeltaLabel(delta ProcessIODelta) string {
	label := firstNonEmpty(delta.Label, delta.Comm, strconv.Itoa(delta.PID))
	if delta.ContainerName != "" {
		label = "container:" + delta.ContainerName
	}
	return fmt.Sprintf("%s pid=%d", label, delta.PID)
}

func diskIOProcessLabel(deltas []ProcessIODelta) string {
	if len(deltas) == 0 {
		return "-"
	}
	return processDeltaLabel(deltas[0])
}

func formatDeviceIOSummary(device DeviceIOSummary) string {
	if device.Device == "" {
		return "-"
	}
	return fmt.Sprintf("%s util=%.2f%% r=%.2fKiB/s w=%.2fKiB/s await=%.2fms",
		device.Device,
		device.UtilPercent,
		device.ReadKiBPerSecond,
		device.WriteKiBPerSecond,
		device.AverageWaitMilliseconds)
}

func firstDeviceIOSummary(devices []DeviceIOSummary) DeviceIOSummary {
	if len(devices) == 0 {
		return DeviceIOSummary{}
	}
	return devices[0]
}

func latestDiskIOAttribution(runs []DiskIOAttributionRun) *DiskIOAttributionRun {
	for i := len(runs) - 1; i >= 0; i-- {
		if len(runs[i].Hosts) == 0 {
			continue
		}
		return &runs[i]
	}
	return nil
}

func writeDiskIOAttributionArtifacts(dir string, run DiskIOAttributionRun, m *RunManifest) error {
	if len(run.Hosts) == 0 {
		return nil
	}
	if err := writeJSONArtifact(dir, "process-io-start.json", processIOSnapshotArtifact(run, "start"), m); err != nil {
		return err
	}
	if err := writeJSONArtifact(dir, "process-io-end.json", processIOSnapshotArtifact(run, "end"), m); err != nil {
		return err
	}
	if err := writeJSONArtifact(dir, "process-io-delta.json", processIODeltaArtifact(run), m); err != nil {
		return err
	}
	if err := writeJSONArtifact(dir, "mounts-window.json", mountInfoArtifact(run), m); err != nil {
		return err
	}
	if err := writeTextArtifact(dir, "pidstat-window.txt", sampleOutputArtifact(run, "pidstat"), m); err != nil {
		return err
	}
	if err := writeTextArtifact(dir, "iostat-window.txt", sampleOutputArtifact(run, "iostat"), m); err != nil {
		return err
	}
	return writeJSONArtifact(dir, "disk-io-attribution.json", run, m)
}

func writeJSONArtifact(dir, name string, value any, m *RunManifest) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	recordArtifactOnce(m, Artifact{Action: strings.TrimSuffix(name, ".json"), Path: path})
	return nil
}

func writeTextArtifact(dir, name, value string, m *RunManifest) error {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	recordArtifactOnce(m, Artifact{Action: strings.TrimSuffix(name, ".txt"), Path: path})
	return nil
}

type processIOSnapshotHostArtifact struct {
	ID        string              `json:"id"`
	Instance  string              `json:"instance,omitempty"`
	SSHHost   string              `json:"ssh_host"`
	Processes []ProcessIOSnapshot `json:"processes,omitempty"`
}

func processIOSnapshotArtifact(run DiskIOAttributionRun, which string) map[string]any {
	hosts := make([]processIOSnapshotHostArtifact, 0, len(run.Hosts))
	for _, host := range run.Hosts {
		processes := host.ProcessStart
		if which == "end" {
			processes = host.ProcessEnd
		}
		hosts = append(hosts, processIOSnapshotHostArtifact{
			ID:        host.ID,
			Instance:  host.Instance,
			SSHHost:   host.SSHHost,
			Processes: processes,
		})
	}
	return map[string]any{
		"start": run.Start,
		"end":   run.End,
		"hosts": hosts,
	}
}

type processIODeltaHostArtifact struct {
	ID                string           `json:"id"`
	Instance          string           `json:"instance,omitempty"`
	SSHHost           string           `json:"ssh_host"`
	ProcessDeltas     []ProcessIODelta `json:"process_io_deltas,omitempty"`
	TopReadProcesses  []ProcessIODelta `json:"top_read_processes,omitempty"`
	TopWriteProcesses []ProcessIODelta `json:"top_write_processes,omitempty"`
}

func processIODeltaArtifact(run DiskIOAttributionRun) map[string]any {
	hosts := make([]processIODeltaHostArtifact, 0, len(run.Hosts))
	for _, host := range run.Hosts {
		hosts = append(hosts, processIODeltaHostArtifact{
			ID:                host.ID,
			Instance:          host.Instance,
			SSHHost:           host.SSHHost,
			ProcessDeltas:     host.ProcessDeltas,
			TopReadProcesses:  host.TopReadProcesses,
			TopWriteProcesses: host.TopWriteProcesses,
		})
	}
	return map[string]any{
		"start":            run.Start,
		"end":              run.End,
		"duration_seconds": run.DurationSeconds,
		"hosts":            hosts,
	}
}

type mountInfoHostArtifact struct {
	ID               string          `json:"id"`
	Instance         string          `json:"instance,omitempty"`
	SSHHost          string          `json:"ssh_host"`
	Mounts           []MountInfo     `json:"mounts,omitempty"`
	DockerContainers []DockerProcess `json:"docker_containers,omitempty"`
}

func mountInfoArtifact(run DiskIOAttributionRun) map[string]any {
	hosts := make([]mountInfoHostArtifact, 0, len(run.Hosts))
	for _, host := range run.Hosts {
		hosts = append(hosts, mountInfoHostArtifact{
			ID:               host.ID,
			Instance:         host.Instance,
			SSHHost:          host.SSHHost,
			Mounts:           host.Mounts,
			DockerContainers: host.DockerContainers,
		})
	}
	return map[string]any{
		"captured_at": run.CapturedAt,
		"hosts":       hosts,
	}
}

func sampleOutputArtifact(run DiskIOAttributionRun, tool string) string {
	var b strings.Builder
	for _, host := range run.Hosts {
		status := host.PidstatStatus
		errText := host.PidstatError
		output := host.PidstatOutput
		if tool == "iostat" {
			status = host.IostatStatus
			errText = host.IostatError
			output = host.IostatOutput
		}
		fmt.Fprintf(&b, "# host=%s instance=%s ssh_host=%s status=%s\n", host.ID, host.Instance, host.SSHHost, status)
		if errText != "" {
			fmt.Fprintf(&b, "# error=%s\n", errText)
		}
		if strings.TrimSpace(output) == "" {
			fmt.Fprintln(&b, "# no output")
		} else {
			b.WriteString(output)
			if !strings.HasSuffix(output, "\n") {
				b.WriteByte('\n')
			}
		}
		fmt.Fprintln(&b)
	}
	return b.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
