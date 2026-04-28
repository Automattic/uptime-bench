// Package certbot builds and runs certbot commands.
package certbot

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Automattic/uptime-bench-certmint/internal/config"
	"github.com/Automattic/uptime-bench-certmint/internal/planner"
)

// Args returns the certbot argv after the binary.
func Args(cfg config.CertbotConfig, order planner.Order) []string {
	args := []string{
		"certonly",
		"--non-interactive",
		"--cert-name", order.CertName,
		"--config-dir", cfg.ConfigDir,
		"--work-dir", cfg.WorkDir,
		"--logs-dir", cfg.LogsDir,
		"--preferred-challenges", "dns",
		"--email", cfg.Email,
		"--agree-tos",
	}
	if cfg.Staging {
		args = append(args, "--staging")
	}
	if cfg.Server != "" {
		args = append(args, "--server", cfg.Server)
	}
	if order.RequiredProfile != "" {
		args = append(args, "--required-profile", order.RequiredProfile)
	} else if order.PreferredProfile != "" {
		args = append(args, "--preferred-profile", order.PreferredProfile)
	}
	args = append(args, cfg.AuthenticatorArgs...)
	args = append(args, cfg.ExtraArgs...)
	for _, identifier := range order.Identifiers {
		args = append(args, "-d", identifier)
	}
	return args
}

// CommandLine formats the certbot command for dry-run output.
func CommandLine(cfg config.CertbotConfig, order planner.Order) string {
	parts := append([]string{cfg.Binary}, Args(cfg, order)...)
	for i, part := range parts {
		parts[i] = strconv.Quote(part)
	}
	return strings.Join(parts, " ")
}

// Run executes certbot and returns combined stdout/stderr.
func Run(ctx context.Context, cfg config.CertbotConfig, order planner.Order) (string, error) {
	cmd := exec.CommandContext(ctx, cfg.Binary, Args(cfg, order)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("certbot: %s: %w", order.CertName, err)
	}
	return string(out), nil
}
