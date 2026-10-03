# Database and query architecture

## Format v1

The single file starts with a 256-byte little-endian header (`GEOCODB1`), format
version 1, feature count, file length, eight section offset/length pairs, section
CRC32C checksums, and a header checksum. Offsets are 64-bit; feature IDs, string
references, and per-feature geometry lengths are 32-bit. A string pool can hold
at most 4 GiB. This is a 64-bit-process format.

Version 1 is the first public database format. Pre-release development snapshots
with other format identifiers must be reimported before use.

| Section | Layout |
| --- | --- |
| Records | 64 bytes/feature: OSM ID/type, polygon flag, admin level, importance, representative point, bounds, geometry and metadata locations |
| Metadata | 44 bytes/feature: eleven interned string references for name, class/type, and address fields |
| Geometry | Path count, then point count and int32 latitude/longitude pairs per path; coordinates scaled by 1e7 |
| Terms | Sorted 24-byte dictionary entries: string offset/length, document frequency, posting offset |
| Strings | Deduplicated length-prefixed UTF-8 strings, shared between metadata and term dictionary |
| Postings | Sorted `(feature ID, field weight)` uint32 pairs; lists over 256 entries append their exact top 100 ranked entries |
| Spatial tree | Packed 32-byte bounding-volume nodes with child indices or leaf ranges; up to 16 features per leaf |
| Spatial order | uint32 feature IDs in spatial-leaf order |

Fixed-width postings favor direct access and galloping intersections over minimum
disk size. JSON is produced only at the HTTP/CLI boundary. Geometry and metadata
are read from a read-only mapping; there is no heap copy of the full database.
Opening validates section bounds, feature offsets, dictionary ordering, and
spatial structure. `Verify` additionally reads every section to check CRC32C and
checks posting order. Checksums detect accidental corruption, not intentional
tampering. Each result copies its returned fields into one owned backing string
so it can outlive the mapping while avoiding a separate allocation per field.

The builder writes a temporary file beside its destination, flushes it, and
creates the destination with a hard link that cannot replace an existing file.
Failed builds remove their temporary file. This gives complete-file publication;
it is not an update log or a multi-writer transaction system. Directory-entry
durability after sudden power loss depends on the host filesystem.

## Forward queries

The dictionary is binary searched after Unicode normalization. Posting lists are
intersected from rarest to most common, using monotonic galloping cursors. A miss
advances the leading cursor to the next possible intersection. Scoring combines
field weights and inverse document frequency with stored importance. A bounded
typed heap retains the best requested results without interface boxing. Filters and proximity bias are evaluated
before retention. Common single-token queries without geographic filters use the
precomputed ranking, which is equivalent to scanning the full list. Prefix
queries merge matching sorted lists and retain the maximum field weight per
document before intersection. There is no silent candidate-count cutoff.

## Reverse queries

A best-first traversal visits spatial nodes by conservative spherical distance
to their bounding boxes. The current kth result tightens the search radius.
Candidates read geometry directly from mapped bytes. Cheap latitude and
longitude lower bounds discard distant line segments before trigonometric work;
polygon crossing checks continue so containment is preserved. Latitude rejection
precedes longitude calculations, and crossing tests calculate longitude only
for segments that straddle the query latitude. Query trigonometric values are
computed once; bounds containing the query longitude use a direct latitude
distance. Haversine-based corner bounds avoid precision loss at short distances.
Dateline-crossing segments get conservative longitude bounds. A typed heap avoids
boxing each spatial visit. Class filters compare the mapped class field directly;
full metadata is decoded only for returned results. All query state is local;
a shared read lock coordinates mapping lifetime
with `Close`, while queries execute concurrently.

## Import

Three PBF passes collect relevant relation memberships, all node coordinates,
then selected way geometries. Parallel PBF decoders preserve input order. Node
coordinates use sorted 16-byte external runs with a one-million-node buffer;
unordered runs are merged. A sparse fence index every 256 nodes narrows binary
searches in the temporary mapped coordinate file. Duplicate node IDs are rejected.

Relations are stitched by matching endpoint node IDs. Ambiguous, incomplete, and
nested relations are counted and skipped. Polygon context is attached using a
separate in-memory spatial tree and prepared crossing buckets. Feature geometry
and metadata remain in RAM until the snapshot is built. Tokenization is cached
for up to 500,000 distinct strings during text-index construction.

## Validation

Tests cover PBF import with both node encodings, out-of-order IDs and object
types, reversed relation members, missing nodes, incomplete relations, polygon
holes, antimeridian queries, ranking and intersections, malformed database/PBF
files, cancellation, HTTP validation, and concurrent query/close behavior.
Randomized tests compare spatial results with brute-force evaluation and verify
that segment pruning and common-token rankings preserve results. The root
package also includes Unicode normalization fuzzing and query benchmarks. A
deterministic 128-address workload samples the whole imported file and supports
recording complete search/reverse results for comparison across implementations.
