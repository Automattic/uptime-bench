package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Automattic/uptime-bench/internal/reportsafety"
)

func main() {
	dir := flag.String("dir", "", "report bundle directory to scan")
	out := flag.String("out", "", "optional output file; defaults to stdout")
	format := flag.String("format", "text", "output format: text or json")
	maxFileBytes := flag.Int64("max-file-bytes", 0, "maximum file size to scan in bytes; default 10 MiB")
	failOnFinding := flag.Bool("fail-on-finding", true, "exit non-zero when findings are present")
	flag.Parse()

	if *dir == "" {
		log.Fatal("report-scan: -dir is required")
	}
	report, err := reportsafety.Scan(reportsafety.Options{
		Root:         *dir,
		MaxFileBytes: *maxFileBytes,
	})
	if err != nil {
		log.Fatalf("report-scan: scan: %v", err)
	}

	var output *os.File
	if *out == "" {
		output = os.Stdout
	} else {
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			log.Fatalf("report-scan: mkdir output dir: %v", err)
		}
		output, err = os.Create(*out)
		if err != nil {
			log.Fatalf("report-scan: create output: %v", err)
		}
		defer output.Close()
	}

	switch *format {
	case "text":
		err = reportsafety.WriteText(output, report)
	case "json":
		err = reportsafety.WriteJSON(output, report)
	default:
		err = fmt.Errorf("unsupported format %q", *format)
	}
	if err != nil {
		log.Fatalf("report-scan: write: %v", err)
	}
	if len(report.Findings) > 0 && *failOnFinding {
		os.Exit(2)
	}
}
