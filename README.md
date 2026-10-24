# geocoding-go

[![Test](https://github.com/ralscha/geocoding-go/actions/workflows/test.yml/badge.svg)](https://github.com/ralscha/geocoding-go/actions/workflows/test.yml)
[![Release](https://github.com/ralscha/geocoding-go/actions/workflows/release.yml/badge.svg)](https://github.com/ralscha/geocoding-go/actions/workflows/release.yml)

Offline forward and reverse geocoding from OpenStreetMap PBF snapshots. Includes
a Go library, an import/query CLI, an HTTP service, and a custom immutable
memory-mapped database.

## Install

Download an archive from [GitHub Releases](https://github.com/ralscha/geocoding-go/releases)
for Linux, macOS, or Windows, on amd64 or arm64. Releases include SHA-256 hashes in
`checksums.txt`. Extract `geocode` (`geocode.exe` on Windows).

To build from source, use the Go version in `go.mod`:

```sh
go install github.com/ralscha/geocoding-go/cmd/geocode@latest
geocode --version
```

Release builds report the version, commit, and build date. A plain `go install`
or `go build` reports `dev` because it does not inject release metadata.

No OSM extract or database is distributed with the software. Download an
`.osm.pbf` snapshot, for example from [Geofabrik](https://download.geofabrik.de/),
then import it once before querying.

## Run on Linux with the release binary

The release binary runs directly on 64-bit Linux. You do not need Go, a C
compiler, or a separate database server.

### Download and extract

Download the Linux archive and `checksums.txt` from the same
[GitHub release](https://github.com/ralscha/geocoding-go/releases). Choose the
architecture reported by `uname -m`:

| `uname -m` | Release archive suffix |
| --- | --- |
| `x86_64` | `linux_amd64.tar.gz` |
| `aarch64` or `arm64` | `linux_arm64.tar.gz` |

Put both downloads in a new directory and open a terminal there. Replace
`1.0.0` and `amd64` below with the version and architecture you downloaded:

```sh
sha256sum --check --ignore-missing checksums.txt
```

Continue only if the downloaded archive reports `OK`. Missing archives for
other platforms are ignored. Then extract the archive and check the binary:

```sh
tar -xzf geocoding-go_1.0.0_linux_amd64.tar.gz
./geocode --version
```

The following commands run from this directory. `./geocode` uses the extracted
binary without installing it into your `PATH`. Keep the included license files
with it.

### Import an OSM snapshot

Download a regional PBF and import it once. For example, for Switzerland:

```sh
curl -fLO https://download.geofabrik.de/europe/switzerland-latest.osm.pbf

# Choose a new output filename for each import.
./geocode import -pbf switzerland-latest.osm.pbf -db switzerland.db
./geocode stats -db switzerland.db -verify
```

Runtime queries need only the database file; the PBF and temporary node files
are not needed after import.

### Query from the terminal

```sh
./geocode search -db switzerland.db -q "Bahnhofstrasse 10 Zurich" -limit 3
./geocode reverse -db switzerland.db -lat 47.3769 -lon 8.5417
```

Both commands print JSON. Add `-class address` to a reverse query to restrict
results to address features. Run `./geocode <command> -h` for all options.

### Run the HTTP server

```sh
./geocode serve -db switzerland.db -addr 127.0.0.1:8080
```

The server stays in the foreground. In another terminal, try:

```sh
curl http://127.0.0.1:8080/health
curl --get http://127.0.0.1:8080/search \
  --data-urlencode 'q=Bahnhofstrasse 10 Zurich' --data-urlencode 'limit=3'
curl 'http://127.0.0.1:8080/reverse?lat=47.3769&lon=8.5417&class=address'
```

Press **Ctrl+C** to shut down gracefully. The example listens only on the local
machine; use `-addr 0.0.0.0:8080` to listen on all network interfaces.

### Import options and database updates

Import options: `-workers` (defaults to up to 8), `-temp` for temporary disk
storage, `-country` and `-country-code` for explicit country fallbacks, and
`-cpuprofile` for a Go CPU profile. Progress and skipped-object counts are printed.
Temporary node coordinates use 16 bytes per node. During run merging, budget
roughly twice that disk space plus the final database. Searchable feature
metadata, geometry, and the text-index builder use RAM proportional to the
imported feature count. This importer has been measured on the supplied country
extract, not the full planet.

Imports refuse to replace an existing file. Build a new snapshot, validate it,
then restart the service with the new path. Files are flushed and published with
an atomic hard link; the output filesystem must support hard links. Never modify
or truncate a mapped database. Queries are read-only and safe to run concurrently.

### Build from source instead

From a source checkout, use a 64-bit Go toolchain matching `go.mod`:

```sh
go test ./...
go build -trimpath -o bin/geocode ./cmd/geocode
./bin/geocode --version
```

Use `./bin/geocode` in place of `./geocode` in the commands above. Database files,
binaries, and input extracts are ignored by Git.

## HTTP

```text
GET /search?q=Bahnhofstrasse%2010%20Zurich&limit=5
GET /search?street=Bahnhofstrasse%2010&city=Zurich
GET /search?q=Zur&prefix=true
GET /reverse?lat=47.3769&lon=8.5417&radius=1000
GET /reverse?lat=47.3769&lon=8.5417&class=address
GET /health
```

Search returns an array; reverse returns one object or HTTP 404. Results contain
OSM identity, numeric latitude/longitude, class/type, importance, a display name,
and address fields. Reverse distances are metres to the feature geometry.

Search accepts `q`, `limit` (1–100), `prefix`, and optional `lat`/`lon` for ranking
near a point. Structured fields are `street`, `city`, `county`, `state`, `country`,
and `postalcode`. They are combined as text tokens. `viewbox=west,north,east,south`
restricts representative points; west greater than east crosses the antimeridian.
`format=json` and `format=jsonv2` are accepted. This is a Nominatim-style subset,
not a drop-in implementation of its API. Unsupported Nominatim options such as
`zoom`, `polygon_geojson`, `accept-language`, and `countrycodes` are not implemented.

The HTTP service has read/write timeouts, a five-second query budget, and graceful
shutdown. The library accepts a caller-supplied `context.Context`.

## Embed

The module is `github.com/ralscha/geocoding-go`; its root package is named `geocode`.

```go
package main

import (
    "context"
    "fmt"
    "log"

    geocode "github.com/ralscha/geocoding-go"
)

func main() {
    db, err := geocode.Open("switzerland.db")
    if err != nil { log.Fatal(err) }
    defer db.Close()

    results, err := db.Search(context.Background(), "Bahnhofstrasse 10 Zürich",
        geocode.SearchOptions{Limit: 5})
    if err != nil { log.Print(err); return }
    fmt.Println(results)

    nearest, err := db.Reverse(context.Background(),
        geocode.Point{Lat: 47.3769, Lon: 8.5417},
        geocode.ReverseOptions{Limit: 1, RadiusMeters: 1000})
    if err != nil { log.Print(err); return }
    fmt.Println(nearest)
}
```

`Build`/`BuildContext` can also create databases from `[]Feature`, independently
of PBF. Returned results own their strings and remain valid after `DB.Close`.
`Close` waits for active queries. Keep one database open for the lifetime of your
process rather than reopening it per request.

## Index and geometry behavior

- Case/accent folding and Unicode tokenization: `Zürich` matches `zurich`, and
  `Straße` matches `strasse`. OSM language names and alternate names are indexed.
- Address-field weights, token rarity, place importance, and optional proximity
  determine ranking. Every query token must match. House numbers, street names,
  postcodes, and administrative context are searchable.
- Optional completion applies to the final token, with a minimum of two runes.
  Prefixes matching more than 256 dictionary terms return an explicit error.
- Reverse search uses line segments and polygon interiors, including holes,
  with an indexed radius search. Administrative areas are omitted unless
  `Class: "boundary"` is requested. `Class: "address"` restricts to imported
  address features; an unrestricted request can return a road or POI.
- Coordinates use signed integers at 1e-7 degree precision. Point distances use
  a sphere of radius 6,371,008.8 m. Line positions use a local equirectangular
  projection followed by spherical distance; this is an approximation, not an
  ellipsoidal surveying calculation. Extremely long segments and polar or
  globe-spanning polygons need additional geometry handling.
- Boundary relations are stitched by node IDs, including reversed member ways
  and inner rings. Prepared latitude buckets accelerate address enrichment.
  Administrative levels 2/4/5–6/7–8 map to country/state/county/city; this mapping
  is a convention and varies by country. Explicit `addr:*` fields take priority.

## Scope

Implemented: named places, named roads, tagged addresses, named POIs, named
buildings, and supported multipolygon/administrative relations. PBF can contain
dense or ordinary nodes, raw or zlib blocks, and unordered IDs/object types.

Not implemented: typo correction, language-specific abbreviation expansion,
address-range interpolation, `associatedStreet` inheritance, nested relation
geometry, incremental OSM replication, mutable transactions, or full Nominatim
ranking/API parity. Incomplete or ambiguous relation rings and ways with missing
node references are skipped and counted. Extracts with clipped boundaries can
therefore lack some address context. History PBFs and compression formats other
than raw/zlib are rejected. All feature metadata and geometry are immutable
between rebuilds.

The custom database implementation is in `database.go`, `search.go`, and
`reverse.go`. External packages provide memory mapping, Unicode normalization,
OSM types, and protobuf support. A small patched copy of the upstream PBF decoder
lives in `internal/pbf` with its license and provenance. The MongoDB driver is a
transitive dependency of the upstream OSM type package; storage here uses the
custom file format.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the file format.

## Docker

Import on the host first, then mount the database into the read-only service:

```sh
docker build -t geocoding-go:dev .
docker run --rm -p 127.0.0.1:8080:8080 --read-only \
  --mount type=bind,source=/absolute/path/switzerland.db,target=/data/geocode.db,readonly \
  geocoding-go:dev
```

The image runs as UID/GID 65532; the database must be readable by that user.
It listens on port 8080 inside the container. Docker Compose uses
`./data/geocode.db` by default; set the `GEOCODE_DB` environment variable to an
existing database path to override it, then run `docker compose up --build -d`.
The HTTP service is available at `http://127.0.0.1:8080`. SIGTERM allows requests
to drain during shutdown. Container images do not contain a dataset.

## Development and releases

Install [Task](https://taskfile.dev/) to use the cross-platform commands:

```sh
task                    # list tasks
task format             # go fix and go fmt
task upgrade            # upgrade, modernize, format, tidy and verify modules
task test               # synthetic PBF fixtures; no external dataset needed
task test:race           # requires a C compiler
task lint               # requires golangci-lint v2.14.0
task bench              # synthetic benchmarks, or set GEOCODE_BENCH_DB
task build              # static executable in dist/
task run -- --version
task release:check      # requires GoReleaser v2.18.2 and a Git checkout
task release:smoke      # six archives, checksum checks and packaged binary tests
task release:verify     # all pre-release checks, including Docker
```

On Windows, select an LLVM/MinGW installation for the race detector:

```powershell
task test:race CGO_DIR=C:/w/ws/preblog/cgo/llvm
```

CI runs Linux race tests, lint, vulnerability checks, and container builds.
Linux, macOS, and Windows jobs also build release archives and run the host
binary through import, database verification, forward/reverse queries, and HTTP.
Pushing a semantic version tag runs these checks before publishing six archives
and checksums with the repository's built-in `GITHUB_TOKEN`.

For real-data benchmarks, set `GEOCODE_BENCH_DB` to your imported database and run
`task bench -- -count=5`. Compare repeated runs on the same machine and query
corpus; synthetic timings alone do not establish production latency.

### Experimental SIMD

Long reverse-query geometries can benefit from Go's experimental
[`simd` package](https://go.dev/blog/simd-experiment). The geometry scan checks
blocks of 64 segments and skips a block only when all 65 vertices lie strictly
outside the latitude window on the same side. The surviving segments use the
existing distance and polygon-containment calculations. Database files and
query results use the same format.

Normal builds retain the original segment scan. To enable block scanning with
vectorized integer min/max on amd64 or arm64, build with Go 1.27 and
`GOEXPERIMENT=simd`:

```sh
GOEXPERIMENT=simd go test ./...
GOEXPERIMENT=simd go build -o bin/geocode ./cmd/geocode
```

In PowerShell, set `$env:GOEXPERIMENT = 'simd'` before the Go commands, and use
`Remove-Item Env:GOEXPERIMENT` to return to normal builds. Unsupported
architectures retain the original scan. On supported architectures, CPUs without
hardware SIMD use scalar block checks. `GODEBUG=simd=0` also selects that fallback.
Release builds keep SIMD disabled; the API remains experimental. CI tests
ordinary, SIMD, and fallback builds.

Measured on 2026-10-24 against `main` at `7a58163`, using Go 1.27.3,
Windows/amd64, and an Intel i7-11800H (AVX-512). These are medians of six
samples per benchmark, `-benchtime=300ms -cpu=1`, with the baseline and
experimental binaries run in rotating order. Database setup is excluded.

| Synthetic workload | Main | SIMD | Time change |
| --- | ---: | ---: | ---: |
| 8,192-segment line traversal, 1 km cutoff | 22.50 µs | 9.67 µs | -57% |
| 8,192-segment polygon traversal, 1 km cutoff | 23.33 µs | 10.73 µs | -54% |
| Full reverse queries around a large polygon, 128 query points | 74.10 µs | 63.27 µs | -15% |
| 64-segment scan, 1 km cutoff | 297 ns | 327 ns | +10% |
| 256-segment scan, 1 km cutoff | 813 ns | 983 ns | +21% |
| 8,192-segment scan without a radius window | 996 µs | 1,151 µs | +16% |
| Mixed text searches over 100,000 point features | 2.29 µs | 2.30 µs | No significant change |

The three improvements above have `p=0.002` in benchstat; allocations are
unchanged (zero for geometry traversal). Some unpruned measurements were noisy,
and the mixed point-only reverse workload was about 3% slower. This is a useful
opt-in experiment for long geometries, not evidence of a general speedup.
These initial measurements used synthetic data; the Switzerland results below
use an imported OSM extract. ARM64 was cross-compiled, but performance was
measured only on this amd64 machine.

[Raw results and benchstat comparisons](benchmarks/simd/comparison.txt) include
the scalar block control (`GODEBUG=simd=0`). That control and the ordinary-build
control were collected in a subsequent round and have separate raw files;
some control measurements have wide confidence intervals.
[Additional controls](benchmarks/simd/controls.txt) compare ordinary builds and
`main` compiled with `GOEXPERIMENT=simd`. Enabling the experiment alone on
`main` substantially slowed long scans on this machine; its cause was not
isolated, so the table uses ordinary `main` as the baseline.
The comparison tool was `golang.org/x/perf/cmd/benchstat` at
`v0.0.0-20261009192801-be2c69fb417e`.

To reproduce, put `geometry_benchmark_test.go` in both the `main` checkout and
this checkout, then run the command below in each, with and without
`GOEXPERIMENT=simd`. Use `GODEBUG=simd=0` with the experimental build for the
scalar block control. Alternate runs between binaries to reduce timing drift.

```sh
go test -run '^$' -bench '^Benchmark(GeometryTraversal|GeometryWindow|ReverseLongGeometry|SearchMixed|ReverseMixed|ReverseAddressMixed)$' -benchmem -benchtime=300ms -count=6 -cpu=1 .
```

The best candidates are long roads and polygon rings where a narrow query
radius excludes most segments. Short address geometries, spherical
trigonometry, indexed posting-list seeks, and PBF varint decoding offer less
direct benefit. Prepared administrative-boundary containment is another
possible target, but its existing latitude buckets already shorten the scan;
it has not been vectorized or benchmarked here.

#### Switzerland extract

`switzerland-261023.osm.pbf` (549,114,239 bytes) was imported with unmodified
`main` at `7a58163`, eight workers, and CPU profiling enabled. Import took
33.77 seconds and produced 3,118,248 features in an 892,913,038-byte database.
Database checksum and posting verification passed. Both implementations use
this same database, stored locally at `.cache/switzerland-261023.db`.

The mixed workloads sample 128 addresses across the database. A second workload
uses a deterministic reservoir sample of 128 non-administrative features with
paths longer than 256 vertices, querying a few metres from a middle vertex.
There are 1,160 eligible features in the extract; sampled paths have 257–4,297
vertices (median 379). The direct geometry benchmark measures those features
with a 1 km cutoff; the full reverse benchmark also includes spatial traversal,
other candidate geometries, ranking, and result construction.

Results use eight samples per build, `-benchtime=1s -cpu=1`, on the same
Windows/amd64 machine as the synthetic run. The run order rotates between
ordinary `main`, `main` with `GOEXPERIMENT=simd`, the scalar block fallback,
and SIMD. Database opening, workload selection, and query warmup are excluded;
these timings describe warm queries, not startup or cold-cache performance.

| Real-data workload | Main | SIMD | Time change |
| --- | ---: | ---: | ---: |
| Mixed reverse queries | 200.7 µs | 112.1 µs | -44.1% |
| Reverse queries near long features | 171.58 µs | 80.16 µs | -53.3% |
| Address-only mixed reverse queries | 65.52 µs | 73.16 µs | +11.7% |
| Mixed forward searches | 12.11 µs | 12.32 µs | No significant change |
| Direct distances to sampled long features | 35.08 µs | 35.33 µs | No significant change |

Both reverse-query improvements have `p<0.001`; the address-only regression
has `p=0.028` and a wide confidence interval. Allocation counts and bytes per
query are unchanged. Scalar batching alone took 177.0 µs for mixed reverse
queries and 126.5 µs near long features, so hardware SIMD reduced those times
by a further 37%. A separate profile of the baseline mixed-reverse benchmark
attributed 67% of CPU samples to `distanceWithin`.

These results support using SIMD for unrestricted reverse queries on this
extract. The address-only regression and the earlier short-geometry results
still warrant keeping it opt-in. The 128-query samples are reproducible
performance probes, not a production traffic distribution or a tail-latency
measurement. [All samples and benchstat comparisons](benchmarks/simd/switzerland/comparison.txt)
include the experiment-only and scalar fallback controls; simply enabling
`GOEXPERIMENT=simd` on unchanged `main` again regressed unrestricted reverse
queries substantially on this machine.

The mixed and long-geometry result snapshots match byte-for-byte between
`main` and SIMD, including ordering, scores, and distances for the sampled
queries. [Dataset metadata and snapshot hashes](benchmarks/simd/switzerland/metadata.json)
record the exact PBF/database checksums and import statistics.

For reproduction, also copy `realdata_benchmark_test.go` into the baseline
checkout. In PowerShell, set an absolute database path before switching
checkouts and repeat the benchmark with the build/runtime settings above:

```powershell
$env:GEOCODE_BENCH_DB = (Resolve-Path .cache/switzerland-261023.db).Path
$env:GOEXPERIMENT = 'simd'
go test -run '^$' -bench '^Benchmark(SearchMixed|ReverseMixed|ReverseAddressMixed|RealLongGeometry|ReverseRealLongFeatures)$' -benchmem -benchtime=1s -count=8 -cpu=1 .
```

Set `GEOCODE_QUERY_SNAPSHOT` and `GEOCODE_GEOMETRY_SNAPSHOT` to output paths,
then run `go test -run 'Test(QueryWorkloadSnapshot|RealGeometrySnapshot)$' .`
in each checkout to reproduce the result comparisons.

### First release

Create the empty `ralscha/geocoding-go` GitHub repository. If this folder has no
Git history yet, initialize it and review the staged source files:

```sh
git init -b main
git add .
git status --short
git commit -m "Initial geocoding-go implementation and release automation"
git remote add origin https://github.com/ralscha/geocoding-go.git
git push -u origin main
```

PBF extracts, databases, caches, and build output are ignored. Enable GitHub
Actions, then verify locally and create a semantic version tag:

```sh
task release:verify
task tag TAG=v1.0.0
task push-tag TAG=v1.0.0
```

On Windows, pass `CGO_DIR=C:/w/ws/preblog/cgo/llvm` to `release:verify` for race
tests. It also requires GoReleaser v2.18.2, golangci-lint v2.14.0, and Docker with
a Linux engine. Task downloads pinned actionlint and govulncheck tools.
GoReleaser requires at least one Git commit even for a local snapshot.

`task release-tag TAG=v1.0.0` combines tag creation and pushing. Tag creation
requires a clean working tree and refuses an existing tag. Commit and push the
branch before tagging; do not reuse a published version tag. Tags such as
`v1.0.0-rc.1` create prereleases. The release workflow needs no custom secret and
publishes GitHub assets; it does not publish container registry images.

`task release:snapshot` creates local archives without publishing and replaces
`dist/`. `task release:smoke` also verifies all six checksums and exercises the
host archive with a generated PBF. Windows archives are ZIP; Linux/macOS archives
are tar.gz. CI runs the native host binary on each OS; the other architectures
are cross-compiled. After publishing, download an archive, verify its checksum,
and run `geocode --version`.

Dependabot checks Go modules and Actions weekly. Keep the Docker Go version
aligned with `go.mod`, and update the pinned release/lint tool versions together
with their documentation. Dependency notices are generated at build time from
the linked modules. Review licenses and embedded copyright headers when changing
dependencies or the copied PBF decoder. Database format changes must document
whether users need to reimport their data. The first public database format is
v1 (`GEOCODB1`); reimport any development snapshot with a different identifier.

## License and OSM data attribution

The software is [MIT licensed](LICENSE), like `tileserver-go`. Release archives
include generated dependency and decoder notices in `NOTICE.txt`; the container
keeps them under `/licenses/`. The local decoder's upstream license is preserved
in `internal/pbf/LICENSE.md`. The software license does not cover OSM datasets.

The input and derived database contain OpenStreetMap data. Preserve attribution
and comply with the applicable [OpenStreetMap data license](https://www.openstreetmap.org/copyright)
when distributing or using them. The HTTP API sends `X-Data-Attribution`; clients
should display attribution in their own interface. The PBF format is documented
by [OpenStreetMap](https://wiki.openstreetmap.org/wiki/PBF_Format).
