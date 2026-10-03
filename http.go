package geocode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const Attribution = "Data © OpenStreetMap contributors, ODbL 1.0. https://www.openstreetmap.org/copyright"

// Handler exposes a documented subset of Nominatim-style search/reverse
// parameters. Each request has a five-second query budget.
func (db *DB) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		db.mu.RLock()
		closed := db.closed
		db.mu.RUnlock()
		if closed {
			writeError(w, http.StatusServiceUnavailable, ErrClosed)
			return
		}
		writeJSON(w, http.StatusOK, db.Stats())
	})
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		if err := validFormat(r); err != nil {
			writeError(w, 400, err)
			return
		}
		q := r.URL.Query()
		text := q.Get("q")
		if text == "" {
			parts := []string{q.Get("street"), q.Get("city"), q.Get("county"), q.Get("state"), q.Get("country"), q.Get("postalcode")}
			text = strings.Join(parts, " ")
		}
		if strings.TrimSpace(text) == "" {
			writeError(w, 400, errors.New("q or structured address fields are required"))
			return
		}
		limit, err := parseLimit(q.Get("limit"), 10)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		opts := SearchOptions{Limit: limit}
		if value := q.Get("prefix"); value != "" {
			opts.Prefix, err = strconv.ParseBool(value)
			if err != nil {
				writeError(w, 400, errors.New("invalid prefix"))
				return
			}
		}
		if q.Has("lat") || q.Has("lon") {
			p, err := parsePoint(q.Get("lat"), q.Get("lon"))
			if err != nil {
				writeError(w, 400, err)
				return
			}
			opts.Near = &p
		}
		if v := q.Get("viewbox"); v != "" {
			parts := strings.Split(v, ",")
			if len(parts) != 4 {
				writeError(w, 400, errors.New("viewbox must be west,north,east,south"))
				return
			}
			var b [4]float64
			for i := range b {
				b[i], err = strconv.ParseFloat(parts[i], 64)
				if err != nil {
					writeError(w, 400, errors.New("invalid viewbox"))
					return
				}
			}
			opts.Bounds = &[4]float64{b[3], b[0], b[1], b[2]}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		results, err := db.Search(ctx, text, opts)
		if err != nil {
			queryError(w, err)
			return
		}
		writeJSON(w, 200, results)
	})
	mux.HandleFunc("GET /reverse", func(w http.ResponseWriter, r *http.Request) {
		if err := validFormat(r); err != nil {
			writeError(w, 400, err)
			return
		}
		q := r.URL.Query()
		p, err := parsePoint(q.Get("lat"), q.Get("lon"))
		if err != nil {
			writeError(w, 400, err)
			return
		}
		radius := 1000.0
		if v := q.Get("radius"); v != "" {
			radius, err = strconv.ParseFloat(v, 64)
			if err != nil {
				writeError(w, 400, errors.New("invalid radius"))
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		results, err := db.Reverse(ctx, p, ReverseOptions{Limit: 1, RadiusMeters: radius, Class: q.Get("class")})
		if err != nil {
			queryError(w, err)
			return
		}
		if len(results) == 0 {
			writeError(w, 404, errors.New("no feature within radius"))
			return
		}
		writeJSON(w, 200, results[0])
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Data-Attribution", Attribution)
		if len(r.URL.RawQuery) > 8192 {
			writeError(w, 414, errors.New("query string too long"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func validFormat(r *http.Request) error {
	v := r.URL.Query().Get("format")
	if v != "" && v != "json" && v != "jsonv2" {
		return errors.New("supported formats: json, jsonv2")
	}
	return nil
}
func parsePoint(lat, lon string) (Point, error) {
	a, e := strconv.ParseFloat(lat, 64)
	if e != nil {
		return Point{}, ErrInvalidCoordinate
	}
	b, e := strconv.ParseFloat(lon, 64)
	p := Point{a, b}
	if e != nil || !validPoint(p) {
		return Point{}, ErrInvalidCoordinate
	}
	return p, nil
}
func parseLimit(v string, def int) (int, error) {
	if v == "" {
		return def, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 1 || n > 100 {
		return 0, fmt.Errorf("limit must be between 1 and 100")
	}
	return n, nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, e error) {
	writeJSON(w, status, map[string]string{"error": e.Error()})
}
func queryError(w http.ResponseWriter, e error) {
	status := 400
	if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
		status = 408
	} else if errors.Is(e, ErrClosed) {
		status = 503
	} else if strings.Contains(e.Error(), "corrupt database") {
		status = 500
	}
	writeError(w, status, e)
}
