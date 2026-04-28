package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/probeips"
)

const defaultServices = probeips.ServicePingdom + "," +
	probeips.ServiceUptimeRobot + "," +
	probeips.ServiceDatadog + "," +
	probeips.ServiceBetterUptime

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("probe-ips-refresh", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	servicesFlag := fs.String("services", defaultServices, "comma-separated services to refresh")
	timeout := fs.Duration("timeout", 15*time.Second, "HTTP fetch timeout")
	pingdomURL := fs.String("pingdom-url", probeips.DefaultPingdomURL, "Pingdom probe IP source URL")
	uptimeRobotURL := fs.String("uptimerobot-url", probeips.DefaultUptimeRobotURL, "UptimeRobot probe IP source URL")
	datadogURL := fs.String("datadog-url", probeips.DefaultDatadogURL, "Datadog IP ranges source URL")
	betterStackURL := fs.String("betterstack-url", probeips.DefaultBetterStackURL, "Better Stack probe IP documentation URL")
	uptimeRobotRegionMap := fs.String("uptimerobot-region-map", "internal/probeips/uptimerobot_regions.json", "optional UptimeRobot region map JSON path")

	if err := fs.Parse(args); err != nil {
		return err
	}

	client := &http.Client{Timeout: *timeout}
	var items []probeips.ServiceRanges
	for _, service := range splitCSV(*servicesFlag) {
		switch service {
		case probeips.ServicePingdom:
			item, err := fetchPingdom(ctx, client, *pingdomURL)
			if err != nil {
				return err
			}
			items = append(items, item)
		case probeips.ServiceUptimeRobot:
			item, err := fetchUptimeRobot(ctx, client, *uptimeRobotURL, *uptimeRobotRegionMap)
			if err != nil {
				return err
			}
			items = append(items, item)
		case probeips.ServiceDatadog:
			item, err := fetchDatadog(ctx, client, *datadogURL)
			if err != nil {
				return err
			}
			items = append(items, item)
		case probeips.ServiceBetterUptime:
			item, err := fetchBetterStack(ctx, client, *betterStackURL)
			if err != nil {
				return err
			}
			items = append(items, item)
		default:
			return fmt.Errorf("unknown service %q", service)
		}
	}
	if len(items) == 0 {
		return errors.New("no services selected")
	}

	_, err := io.WriteString(out, probeips.FormatTOML(items))
	return err
}

func fetchPingdom(ctx context.Context, client *http.Client, sourceURL string) (probeips.ServiceRanges, error) {
	data, err := fetch(ctx, client, sourceURL)
	if err != nil {
		return probeips.ServiceRanges{}, fmt.Errorf("pingdom: %w", err)
	}
	regions, warnings, err := probeips.ParsePingdom(data)
	if err != nil {
		return probeips.ServiceRanges{}, fmt.Errorf("pingdom: %w", err)
	}
	return probeips.ServiceRanges{
		ServiceID:   probeips.ServicePingdom,
		ServiceType: probeips.ServicePingdom,
		SourceURL:   sourceURL,
		Regions:     regions,
		Warnings:    warnings,
	}, nil
}

func fetchUptimeRobot(ctx context.Context, client *http.Client, sourceURL, regionMapPath string) (probeips.ServiceRanges, error) {
	data, err := fetch(ctx, client, sourceURL)
	if err != nil {
		return probeips.ServiceRanges{}, fmt.Errorf("uptimerobot: %w", err)
	}
	regionMap, warnings, err := loadRegionMap(regionMapPath)
	if err != nil {
		return probeips.ServiceRanges{}, fmt.Errorf("uptimerobot: %w", err)
	}
	regions, parseWarnings, err := probeips.ParseUptimeRobot(data, regionMap)
	if err != nil {
		return probeips.ServiceRanges{}, fmt.Errorf("uptimerobot: %w", err)
	}
	warnings = append(warnings, parseWarnings...)
	return probeips.ServiceRanges{
		ServiceID:   probeips.ServiceUptimeRobot,
		ServiceType: probeips.ServiceUptimeRobot,
		SourceURL:   sourceURL,
		Regions:     regions,
		Warnings:    warnings,
	}, nil
}

func fetchDatadog(ctx context.Context, client *http.Client, sourceURL string) (probeips.ServiceRanges, error) {
	data, err := fetch(ctx, client, sourceURL)
	if err != nil {
		return probeips.ServiceRanges{}, fmt.Errorf("datadog-synthetics: %w", err)
	}
	regions, warnings, err := probeips.ParseDatadog(data)
	if err != nil {
		return probeips.ServiceRanges{}, fmt.Errorf("datadog-synthetics: %w", err)
	}
	return probeips.ServiceRanges{
		ServiceID:   probeips.ServiceDatadog,
		ServiceType: probeips.ServiceDatadog,
		SourceURL:   sourceURL,
		Regions:     regions,
		Warnings:    warnings,
	}, nil
}

func fetchBetterStack(ctx context.Context, client *http.Client, sourceURL string) (probeips.ServiceRanges, error) {
	data, err := fetch(ctx, client, sourceURL)
	if err != nil {
		return probeips.ServiceRanges{}, fmt.Errorf("better-uptime: %w", err)
	}
	regions, warnings, err := probeips.ParseBetterStack(data)
	if err != nil {
		return probeips.ServiceRanges{}, fmt.Errorf("better-uptime: %w", err)
	}
	return probeips.ServiceRanges{
		ServiceID:   probeips.ServiceBetterUptime,
		ServiceType: probeips.ServiceBetterUptime,
		SourceURL:   sourceURL,
		Regions:     regions,
		Warnings:    warnings,
	}, nil
}

func fetch(ctx context.Context, client *http.Client, sourceURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s returned %s", sourceURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func loadRegionMap(path string) (probeips.RegionMap, []string, error) {
	if strings.TrimSpace(path) == "" {
		return probeips.RegionMap{}, []string{"UptimeRobot region map disabled; emitted untagged prefixes under global"}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return probeips.RegionMap{}, []string{fmt.Sprintf("UptimeRobot region map %s not found; emitted untagged prefixes under global", path)}, nil
	}
	if err != nil {
		return probeips.RegionMap{}, nil, fmt.Errorf("read region map %s: %w", path, err)
	}
	regionMap, err := probeips.ParseRegionMap(data)
	if err != nil {
		return probeips.RegionMap{}, nil, err
	}
	return regionMap, nil, nil
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
