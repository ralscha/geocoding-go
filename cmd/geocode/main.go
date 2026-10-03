package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	geocode "github.com/ralscha/geocoding-go"
)

// Set by GoReleaser and container builds through -ldflags.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

const usage = "usage: geocode <import|search|reverse|serve|stats|version> [options]"

func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func output(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New(usage)
	}
	switch os.Args[1] {
	case "version", "--version", "-version":
		fmt.Printf("geocode %s (commit %s, built %s, %s %s/%s)\n", version, commit, date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	case "help", "--help", "-h":
		fmt.Println(usage)
		fmt.Println("Run geocode <command> -h for command options.")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	path := fs.String("db", "geocode.db", "embedded database path")
	switch os.Args[1] {
	case "import":
		pbf := fs.String("pbf", "", "OSM PBF snapshot path (required)")
		workers := fs.Int("workers", min(runtime.GOMAXPROCS(0), 8), "parallel PBF decoders and enrichment workers")
		temp := fs.String("temp", "", "temporary node-store directory")
		country := fs.String("country", "", "fallback country name")
		code := fs.String("country-code", "", "fallback ISO country code")
		cpu := fs.String("cpuprofile", "", "write CPU profile")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		if *pbf == "" {
			return errors.New("-pbf is required")
		}
		if *cpu != "" {
			f, e := os.Create(*cpu)
			if e != nil {
				return e
			}
			defer f.Close()
			if e = pprof.StartCPUProfile(f); e != nil {
				return e
			}
			defer pprof.StopCPUProfile()
		}
		stats, e := geocode.ImportPBF(ctx, *pbf, *path, geocode.ImportOptions{Workers: *workers, TempDir: *temp, Country: *country, CountryCode: *code, Progress: func(p geocode.ImportProgress) {
			fmt.Fprintf(os.Stderr, "%-20s objects=%d features=%d elapsed=%s\n", p.Stage, p.Objects, p.Features, p.Elapsed.Round(time.Millisecond))
		}})
		if e != nil {
			return e
		}
		return output(stats)
	case "search":
		q := fs.String("q", "", "address or place name")
		limit := fs.Int("limit", 10, "maximum results")
		prefix := fs.Bool("prefix", false, "complete the final token")
		lat := fs.Float64("lat", math.NaN(), "nearby latitude for ranking")
		lon := fs.Float64("lon", math.NaN(), "nearby longitude for ranking")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		db, e := geocode.Open(*path)
		if e != nil {
			return e
		}
		defer db.Close()
		opts := geocode.SearchOptions{Limit: *limit, Prefix: *prefix}
		if !math.IsNaN(*lat) || !math.IsNaN(*lon) {
			opts.Near = &geocode.Point{Lat: *lat, Lon: *lon}
		}
		r, e := db.Search(ctx, *q, opts)
		if e != nil {
			return e
		}
		return output(r)
	case "reverse":
		lat := fs.Float64("lat", math.NaN(), "latitude (required)")
		lon := fs.Float64("lon", math.NaN(), "longitude (required)")
		limit := fs.Int("limit", 1, "maximum results")
		radius := fs.Float64("radius", 1000, "search radius in metres")
		class := fs.String("class", "", "optional feature class")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		db, e := geocode.Open(*path)
		if e != nil {
			return e
		}
		defer db.Close()
		r, e := db.Reverse(ctx, geocode.Point{Lat: *lat, Lon: *lon}, geocode.ReverseOptions{Limit: *limit, RadiusMeters: *radius, Class: *class})
		if e != nil {
			return e
		}
		return output(r)
	case "stats":
		verify := fs.Bool("verify", false, "verify all section checksums and postings")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		db, e := geocode.Open(*path)
		if e != nil {
			return e
		}
		defer db.Close()
		if *verify {
			if e = db.Verify(); e != nil {
				return e
			}
		}
		return output(db.Stats())
	case "serve":
		addr := fs.String("addr", "127.0.0.1:8080", "HTTP listen address")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		db, e := geocode.Open(*path)
		if e != nil {
			return e
		}
		defer db.Close()
		server := &http.Server{Addr: *addr, Handler: db.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if e := server.Shutdown(shutdown); e != nil {
				_ = server.Close()
			}
		}()
		fmt.Fprintf(os.Stderr, "geocode listening on http://%s (%d features)\n", *addr, db.Stats().Features)
		e = server.ListenAndServe()
		stop()
		<-done
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}
