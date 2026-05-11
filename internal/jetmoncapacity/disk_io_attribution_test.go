package jetmoncapacity

import (
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
)

func TestProcessIODeltasLabelsContainersAndComputesRates(t *testing.T) {
	containers := []DockerProcess{{
		ID:      "abcdef1234567890",
		Name:    "mysql-1",
		HostPID: 321,
	}}
	startData := []byte("pid\tstart_time_ticks\tcomm\tcmdline\tcgroup\trchar\twchar\tsyscr\tsyscw\tread_bytes\twrite_bytes\tcancelled_write_bytes\n" +
		"123\t100\tjetmon2\t/usr/local/bin/jetmon2\t0::/system.slice/jetmon2.service\t1000\t2000\t10\t20\t4096\t8192\t0\n" +
		"321\t200\tmysqld\tmysqld --daemon\t0::/docker/abcdef1234567890\t500\t1000\t5\t10\t1024\t2048\t0\n")
	endData := []byte("pid\tstart_time_ticks\tcomm\tcmdline\tcgroup\trchar\twchar\tsyscr\tsyscw\tread_bytes\twrite_bytes\tcancelled_write_bytes\n" +
		"123\t100\tjetmon2\t/usr/local/bin/jetmon2\t0::/system.slice/jetmon2.service\t3000\t7000\t20\t45\t12288\t24576\t0\n" +
		"321\t200\tmysqld\tmysqld --daemon\t0::/docker/abcdef1234567890\t1500\t3000\t9\t18\t4096\t6144\t0\n")

	start, err := parseProcessIOSnapshots(startData)
	if err != nil {
		t.Fatalf("parse start: %v", err)
	}
	end, err := parseProcessIOSnapshots(endData)
	if err != nil {
		t.Fatalf("parse end: %v", err)
	}
	labelProcessIOSnapshots(start, containers, []string{"jetmon2", "mysqld"})
	labelProcessIOSnapshots(end, containers, []string{"jetmon2", "mysqld"})

	deltas := processIODeltas(start, end, 10*time.Second)
	if len(deltas) != 2 {
		t.Fatalf("deltas = %d, want 2: %+v", len(deltas), deltas)
	}
	var jetmon, mysql ProcessIODelta
	for _, delta := range deltas {
		switch delta.PID {
		case 123:
			jetmon = delta
		case 321:
			mysql = delta
		}
	}
	if jetmon.Label != "jetmon2" || jetmon.Delta.ReadBytes != 8192 || jetmon.WriteBytesPerSecond != 1638.4 {
		t.Fatalf("jetmon delta = %+v, want label jetmon2 and byte deltas/rates", jetmon)
	}
	if mysql.Label != "container:mysql-1" || mysql.ContainerName != "mysql-1" || mysql.Delta.ReadBytes != 3072 {
		t.Fatalf("mysql delta = %+v, want container attribution", mysql)
	}
}

func TestAnnotateDiskIOAttributionFlagsUnattributedHostIO(t *testing.T) {
	run := DiskIOAttributionRun{
		Status:          "pass",
		Start:           time.Unix(1, 0).UTC(),
		End:             time.Unix(11, 0).UTC(),
		DurationSeconds: 10,
		Hosts: []DiskIOAttributionHost{{
			ID:       "jetmon-v2",
			Instance: "jetmon-v2.example.com",
			Status:   "pass",
			ProcessDeltas: []ProcessIODelta{{
				PID:                 123,
				Label:               "jetmon2",
				ReadBytesPerSecond:  1024,
				WriteBytesPerSecond: 2048,
			}},
			TopReadProcesses: []ProcessIODelta{{
				PID:                123,
				Label:              "jetmon2",
				ReadBytesPerSecond: 1024,
			}},
			TopWriteProcesses: []ProcessIODelta{{
				PID:                 123,
				Label:               "jetmon2",
				WriteBytesPerSecond: 2048,
			}},
			DeviceIO: []DeviceIOSummary{{
				Device:                  "vda",
				Samples:                 2,
				ReadKiBPerSecond:        100,
				WriteKiBPerSecond:       200,
				AverageWaitMilliseconds: 1.5,
				UtilPercent:             20,
			}},
		}},
	}
	report := &capacitybench.Report{Summaries: []capacitybench.SeriesSummary{
		{Query: "host_disk_read_bytes", Unit: "bytes_per_second", Labels: map[string]string{"instance": "jetmon-v2.example.com"}, Avg: 20 * 1024 * 1024},
		{Query: "host_disk_written_bytes", Unit: "bytes_per_second", Labels: map[string]string{"instance": "jetmon-v2.example.com"}, Avg: 3 * 1024 * 1024},
		{Query: "docker_container_block_read_bytes", Unit: "bytes_per_second", Labels: map[string]string{"instance": "jetmon-v2.example.com", "container": "mysql"}, Avg: 1024},
		{Query: "docker_container_block_write_bytes", Unit: "bytes_per_second", Labels: map[string]string{"instance": "jetmon-v2.example.com", "container": "mysql"}, Avg: 2048},
	}}

	annotateDiskIOAttribution(&run, report)

	if run.Status != "warning" {
		t.Fatalf("run status = %q, want warning", run.Status)
	}
	if len(run.Summaries) != 1 || run.Summaries[0].Status != "mismatch" {
		t.Fatalf("summary = %+v, want mismatch", run.Summaries)
	}
	if !strings.Contains(strings.Join(run.Warnings, " "), "host read rate") {
		t.Fatalf("warnings = %#v, want read mismatch", run.Warnings)
	}
	if run.Summaries[0].TopReadProcess != "jetmon2 pid=123" {
		t.Fatalf("top read process = %q, want jetmon label", run.Summaries[0].TopReadProcess)
	}
	if !strings.Contains(run.Summaries[0].TopDevice, "vda util=20.00%") {
		t.Fatalf("top device = %q, want vda summary", run.Summaries[0].TopDevice)
	}
}

func TestParseIostatDeviceSummariesAveragesSamples(t *testing.T) {
	output := `
Linux 6.8.0

Device            r/s     rkB/s   w/s     wkB/s  await  %util
loop0            99.00  999.00  99.00  999.00  9.00   99.00
vda              10.00  100.00  20.00  200.00  1.00   30.00
vdb               1.00   10.00   2.00   20.00  2.00   10.00

Device            r/s     rkB/s   w/s     wkB/s  await  %util
vda              30.00  300.00  40.00  400.00  3.00   50.00
vdb               3.00   30.00   4.00   40.00  4.00   20.00
`
	devices := parseIostatDeviceSummaries(output)
	if len(devices) != 2 {
		t.Fatalf("devices = %d, want 2: %+v", len(devices), devices)
	}
	if devices[0].Device != "vda" || devices[0].Samples != 2 {
		t.Fatalf("top device = %+v, want vda with 2 samples", devices[0])
	}
	if devices[0].ReadOpsPerSecond != 20 || devices[0].WriteKiBPerSecond != 300 || devices[0].AverageWaitMilliseconds != 2 || devices[0].UtilPercent != 40 {
		t.Fatalf("vda averages = %+v, want averaged iostat fields", devices[0])
	}
}
