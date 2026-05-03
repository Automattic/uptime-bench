package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/fleet"
)

type harnessArtifacts struct {
	dir         string
	oldLog      io.Writer
	logFiles    []*os.File
	logRestored bool
}

type controllerRunResult struct {
	Kind      string
	ID        string
	RunID     string
	StartedAt time.Time
	EndedAt   time.Time
	Err       error
}

type fleetStatusSnapshot struct {
	CapturedAt time.Time           `json:"captured_at"`
	Members    []fleetMemberStatus `json:"members"`
}

type fleetMemberStatus struct {
	Role        string                  `json:"role"`
	ConfigIDs   []string                `json:"config_ids"`
	Address     string                  `json:"address"`
	ControlPort int                     `json:"control_port"`
	ControlURL  string                  `json:"control_url"`
	Status      *control.StatusResponse `json:"status,omitempty"`
	Error       string                  `json:"error,omitempty"`
}

func setupHarnessArtifacts(dir string) (*harnessArtifacts, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		return nil, fmt.Errorf("create logs dir: %w", err)
	}

	a := &harnessArtifacts{
		dir:      dir,
		oldLog:   log.Writer(),
		logFiles: make([]*os.File, 0, 2),
	}
	for _, name := range []string{filepath.Join("logs", "harness.log"), "controller.log"} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			_ = a.Close()
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		a.logFiles = append(a.logFiles, f)
	}

	writers := []io.Writer{a.oldLog}
	for _, f := range a.logFiles {
		writers = append(writers, f)
	}
	log.SetOutput(io.MultiWriter(writers...))
	log.Printf("harness: writing controller artifacts to %s", dir)
	return a, nil
}

func (a *harnessArtifacts) Close() error {
	if a == nil {
		return nil
	}
	if !a.logRestored {
		log.SetOutput(a.oldLog)
		a.logRestored = true
	}
	var errs []error
	for _, f := range a.logFiles {
		if err := f.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	a.logFiles = nil
	return errors.Join(errs...)
}

func (a *harnessArtifacts) CopyInput(kind, path string) error {
	if a == nil {
		return nil
	}
	var subdir string
	switch kind {
	case "scenario":
		subdir = "scenarios"
	case "campaign":
		subdir = "campaigns"
	default:
		return fmt.Errorf("unknown input kind %q", kind)
	}
	dst, err := copyArtifactFile(path, filepath.Join(a.dir, subdir))
	if err != nil {
		return err
	}
	log.Printf("harness: copied %s input to %s", kind, dst)
	return nil
}

func (a *harnessArtifacts) WriteRunResult(result controllerRunResult) error {
	if a == nil {
		return nil
	}
	path := filepath.Join(a.dir, "run-results.tsv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	needsHeader := true
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		needsHeader = false
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if needsHeader {
		if _, err := fmt.Fprintln(f, strings.Join([]string{
			"kind",
			"id",
			"run_id",
			"started_at_utc",
			"ended_at_utc",
			"status",
			"error",
		}, "\t")); err != nil {
			return err
		}
	}

	status := "ok"
	errText := ""
	if result.Err != nil {
		status = "failed"
		errText = result.Err.Error()
	}
	row := []string{
		result.Kind,
		result.ID,
		result.RunID,
		result.StartedAt.UTC().Format(time.RFC3339Nano),
		result.EndedAt.UTC().Format(time.RFC3339Nano),
		status,
		errText,
	}
	for i := range row {
		row[i] = escapeHarnessTSVField(row[i])
	}
	_, err = fmt.Fprintln(f, strings.Join(row, "\t"))
	return err
}

func (a *harnessArtifacts) WriteTargetStatusAfter(fl *fleet.Config) error {
	if a == nil {
		return nil
	}
	token, err := readControlToken(fl)
	if err != nil {
		return fmt.Errorf("read control token for target status: %w", err)
	}
	snapshot := collectFleetStatus(context.Background(), fl, token, time.Now().UTC())
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(a.dir, "target-status-after.json"), append(data, '\n')); err != nil {
		return err
	}
	memberErrors := 0
	for _, member := range snapshot.Members {
		if member.Error != "" {
			memberErrors++
		}
	}
	if memberErrors > 0 {
		log.Printf("harness: target status snapshot captured with %d member error(s)", memberErrors)
	} else {
		log.Printf("harness: target status snapshot captured for %d member(s)", len(snapshot.Members))
	}
	return nil
}

func collectFleetStatus(ctx context.Context, fl *fleet.Config, token string, capturedAt time.Time) fleetStatusSnapshot {
	type member struct {
		role        string
		configID    string
		address     string
		controlPort int
	}
	members := make([]member, 0, len(fl.Targets)+len(fl.Nameservers))
	for _, t := range fl.Targets {
		members = append(members, member{
			role:        "target",
			configID:    t.ID,
			address:     t.Address,
			controlPort: t.ControlPort,
		})
	}
	for _, ns := range fl.Nameservers {
		members = append(members, member{
			role:        "nameserver",
			configID:    ns.ID,
			address:     ns.Address,
			controlPort: ns.ControlPort,
		})
	}

	type aggregate struct {
		status fleetMemberStatus
	}
	ordered := make([]string, 0, len(members))
	aggregates := make(map[string]*aggregate, len(members))
	for _, m := range members {
		controlURL := fmt.Sprintf("http://%s:%d", m.address, m.controlPort)
		key := m.role + "\x00" + controlURL
		agg, ok := aggregates[key]
		if !ok {
			agg = &aggregate{status: fleetMemberStatus{
				Role:        m.role,
				Address:     m.address,
				ControlPort: m.controlPort,
				ControlURL:  controlURL,
			}}
			aggregates[key] = agg
			ordered = append(ordered, key)
		}
		agg.status.ConfigIDs = append(agg.status.ConfigIDs, m.configID)
	}

	timeout := fl.Control.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}
	out := fleetStatusSnapshot{
		CapturedAt: capturedAt.UTC(),
		Members:    make([]fleetMemberStatus, 0, len(ordered)),
	}
	for _, key := range ordered {
		status := aggregates[key].status
		memberCtx, cancel := context.WithTimeout(ctx, timeout)
		resp, err := control.NewClient(status.ControlURL, token, httpClient).Status(memberCtx)
		cancel()
		if err != nil {
			status.Error = err.Error()
		} else {
			status.Status = resp
		}
		out.Members = append(out.Members, status)
	}
	return out
}

func copyArtifactFile(src, dstDir string) (string, error) {
	base := filepath.Base(src)
	if base == "." || base == string(filepath.Separator) || strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("invalid source path %q", src)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dstDir, base)
	if err := writeFileAtomic(dst, data); err != nil {
		return "", err
	}
	return dst, nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func escapeHarnessTSVField(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"\t", `\t`,
		"\n", `\n`,
		"\r", `\r`,
	)
	return replacer.Replace(s)
}
