package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type Config struct {
	ShowAll       bool
	HumanReadable bool
	OutputFormat  string
	SortBy        string
	ExcludeTypes  string
	WarnThreshold float64
	CritThreshold float64
	NoColor       bool
}

type FS struct {
	Device string  `json:"device"`
	Mount  string  `json:"mount"`
	Type   string  `json:"type"`
	Total  uint64  `json:"total"`
	Free   uint64  `json:"free"`
	Used   uint64  `json:"used"`
	Usage  float64 `json:"usage"`
}

type ColorScheme struct {
	Low      string
	Medium   string
	High     string
	Critical string
	Reset    string
}

var Colors = ColorScheme{
	Low:      "\033[32m",
	Medium:   "\033[33m",
	High:     "\033[31m",
	Critical: "\033[31;1m",
	Reset:    "\033[0m",
}

func main() {
	config := parseFlags()
	logger := log.New(os.Stderr, "dfmon: ", log.Lshortfile)

	if _, err := os.Stat("/proc/mounts"); err != nil {
		logger.Fatal("Linux only: /proc/mounts not found")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		cancel()
	}()

	mounts, err := readMounts()
	if err != nil {
		logger.Fatalf("Failed to read mounts: %v", err)
	}

	excludeTypes := strings.Split(config.ExcludeTypes, ",")
	filteredMounts := filterMounts(mounts, excludeTypes)
	data := analyze(filteredMounts, logger, ctx)
	sortFS(data, config.SortBy)
	display(data, config)
}

func parseFlags() Config {
	var config Config
	flag.BoolVar(&config.ShowAll, "a", false, "Show all filesystems")
	flag.BoolVar(&config.HumanReadable, "h", true, "Human readable sizes")
	flag.StringVar(&config.OutputFormat, "o", "table", "Output format (table, json, csv)")
	flag.StringVar(&config.SortBy, "s", "mount", "Sort by (mount, usage, size)")
	flag.StringVar(&config.ExcludeTypes, "x", "proc,sysfs,devtmpfs,tmpfs,cgroup,devpts", "Exclude filesystem types")
	flag.Float64Var(&config.WarnThreshold, "w", 70, "Warning threshold")
	flag.Float64Var(&config.CritThreshold, "c", 90, "Critical threshold")
	flag.BoolVar(&config.NoColor, "no-color", false, "Disable color output")
	flag.Parse()
	return config
}

func readMounts() ([][]string, error) {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var out [][]string
	for _, l := range lines {
		p := strings.Fields(l)
		if len(p) >= 3 {
			out = append(out, []string{p[0], p[1], p[2]})
		}
	}
	return out, nil
}

func shouldIncludeFS(fsType string, excludeTypes []string) bool {
	for _, ex := range excludeTypes {
		if ex != "" && fsType == ex {
			return false
		}
	}
	return true
}

func filterMounts(mounts [][]string, excludeTypes []string) [][]string {
	var filtered [][]string
	for _, m := range mounts {
		if shouldIncludeFS(m[2], excludeTypes) {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

func analyze(mounts [][]string, logger *log.Logger, ctx context.Context) []FS {
	var list []FS

	for i, m := range mounts {
		select {
		case <-ctx.Done():
			logger.Printf("Analysis cancelled")
			return list
		default:
		}

		if i%10 == 0 {
			logger.Printf("Processing %d/%d mounts...", i, len(mounts))
		}

		var s syscall.Statfs_t
		if err := syscall.Statfs(m[1], &s); err != nil {
			logger.Printf("Warning: cannot stat %s: %v", m[1], err)
			continue
		}

		total := s.Blocks * uint64(s.Bsize)
		free := s.Bavail * uint64(s.Bsize)
		used := total - free
		usage := 0.0
		if total > 0 {
			usage = float64(used) / float64(total) * 100
		}

		list = append(list, FS{
			Device: m[0],
			Mount:  m[1],
			Type:   m[2],
			Total:  total,
			Free:   free,
			Used:   used,
			Usage:  usage,
		})
	}
	return list
}

func fmtBytes(b uint64, humanReadable bool) string {
	if !humanReadable {
		return fmt.Sprintf("%d", b)
	}

	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}

	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	exp := math.Log(float64(b)) / math.Log(1024)
	idx := int(exp)
	if idx >= len(units) {
		idx = len(units) - 1
	}

	val := float64(b) / math.Pow(1024, float64(idx))
	return fmt.Sprintf("%.1f %s", val, units[idx])
}

func (c ColorScheme) ForUsage(usage, warn, crit float64, noColor bool) string {
	if noColor {
		return ""
	}
	switch {
	case usage >= crit:
		return c.Critical
	case usage >= warn:
		return c.High
	case usage >= 70:
		return c.Medium
	default:
		return c.Low
	}
}

func sortFS(list []FS, by string) {
	sort.Slice(list, func(i, j int) bool {
		switch by {
		case "mount":
			return list[i].Mount < list[j].Mount
		case "usage":
			return list[i].Usage > list[j].Usage
		case "size":
			return list[i].Total > list[j].Total
		default:
			return list[i].Mount < list[j].Mount
		}
	})
}

func display(list []FS, config Config) {
	switch config.OutputFormat {
	case "json":
		displayJSON(list)
	case "csv":
		displayCSV(list)
	default:
		displayTable(list, config)
	}
}

func displayJSON(list []FS) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(list); err != nil {
		log.Printf("JSON encoding error: %v", err)
	}
}

func displayCSV(list []FS) {
	fmt.Println("Device,Mount,Type,Total,Used,Free,Usage")
	for _, d := range list {
		fmt.Printf("%s,%s,%s,%d,%d,%d,%.2f\n",
			d.Device, d.Mount, d.Type, d.Total, d.Used, d.Free, d.Usage)
	}
}

func displayTable(list []FS, config Config) {
	fmt.Printf("%-25s %-25s %-8s %-10s %-10s %-10s %s\n",
		"Device", "Mount", "Type", "Total", "Used", "Free", "Usage")
	
	for _, d := range list {
		color := Colors.ForUsage(d.Usage, config.WarnThreshold, config.CritThreshold, config.NoColor)
		reset := ""
		if color != "" {
			reset = Colors.Reset
		}
		
		fmt.Printf("%-25s %-25s %-8s %-10s %-10s %-10s %s%s%%%s\n",
			d.Device, d.Mount, d.Type,
			fmtBytes(d.Total, config.HumanReadable),
			fmtBytes(d.Used, config.HumanReadable),
			fmtBytes(d.Free, config.HumanReadable),
			color, strconv.FormatFloat(d.Usage, 'f', 2, 64), reset,
		)
	}
}
