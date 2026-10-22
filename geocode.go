// Package geocode provides an offline OSM geocoder backed by an immutable,
// memory-mapped database. Queries are safe for concurrent use.
package geocode

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
)

const earthRadius = 6371008.8

var (
	ErrClosed            = errors.New("geocode: database is closed")
	ErrInvalidCoordinate = errors.New("geocode: invalid latitude or longitude")
	ErrQueryTooBroad     = errors.New("geocode: prefix matches more than 256 terms; enter more characters")
)

// Point is a WGS84 coordinate in degrees.
type Point struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type Address struct {
	HouseNumber string `json:"house_number,omitempty"`
	Street      string `json:"road,omitempty"`
	Postcode    string `json:"postcode,omitempty"`
	City        string `json:"city,omitempty"`
	County      string `json:"county,omitempty"`
	State       string `json:"state,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
}

// Feature is the import representation. Geometry paths are lines, or polygon
// rings when Polygon is true. Rings are implicitly closed and use even/odd fill
// (including holes).
type Feature struct {
	OSMID      int64     `json:"osm_id"`
	OSMType    string    `json:"osm_type"`
	Name       string    `json:"name,omitempty"`
	Aliases    []string  `json:"aliases,omitempty"`
	Class      string    `json:"class"`
	Type       string    `json:"type"`
	Address    Address   `json:"address"`
	Point      Point     `json:"location"`
	Geometry   [][]Point `json:"-"`
	Polygon    bool      `json:"-"`
	AdminLevel int       `json:"-"`
	Importance float64   `json:"importance"`
}

type Result struct {
	OSMID       int64   `json:"osm_id"`
	OSMType     string  `json:"osm_type"`
	Name        string  `json:"name,omitempty"`
	DisplayName string  `json:"display_name"`
	Class       string  `json:"class"`
	Type        string  `json:"type"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Address     Address `json:"address"`
	Importance  float64 `json:"importance"`
	Distance    float64 `json:"distance_m,omitempty"`
	Score       float64 `json:"score,omitempty"`
}

type SearchOptions struct {
	Limit int
	// Prefix enables completion of the final token, with a minimum of 2 runes.
	Prefix bool
	// Near biases ranking toward this point; it does not restrict results.
	Near *Point
	// Bounds restricts representative points: south, west, north, east.
	// west > east expresses a box crossing the antimeridian.
	Bounds *[4]float64
}

type ReverseOptions struct {
	Limit int
	// RadiusMeters defaults to 1000. The maximum is half Earth's circumference.
	RadiusMeters float64
	// Class optionally restricts results, e.g. "address" or "highway".
	Class string
}

func validPoint(p Point) bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lon) && math.Abs(p.Lat) <= 90 && math.Abs(p.Lon) <= 180
}
func queryLimit(n int) int {
	if n <= 0 {
		return 10
	}
	return min(n, 100)
}
func checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func displayName(f Feature) string {
	a := f.Address
	street := a.Street
	if street == "" {
		street = a.HouseNumber
	} else if a.HouseNumber != "" {
		street += " " + a.HouseNumber
	}
	street = strings.TrimSpace(street)
	parts := []string{f.Name, street, a.Postcode, a.City, a.County, a.State, a.Country}
	out := parts[:0]
	for _, p := range parts {
		if p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

func featureResult(f Feature) Result {
	return Result{OSMID: f.OSMID, OSMType: f.OSMType, Name: f.Name, DisplayName: displayName(f), Class: f.Class, Type: f.Type, Lat: f.Point.Lat, Lon: f.Point.Lon, Address: f.Address, Importance: f.Importance}
}
