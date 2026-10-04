package geta

import (
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// deprecationPlan holds the Deprecation and Sunset header values an
// operation sends on every response.
type deprecationPlan struct {
	deprecation string // RFC 9745: an sf-date, "@" and the seconds since the epoch
	sunset      string // RFC 8594: an HTTP-date (IMF-fixdate)
}

// The header names, in canonical form.
const (
	deprecationHeader = "Deprecation"
	sunsetHeader      = "Sunset"
)

// planDeprecation checks c's Doc.Deprecation and Doc.Sunset and sets
// c.deprecated. It must run after c's output, rows, and chain are known, to
// refuse a header of the same name declared by any of them.
func (c *compiledOp) planDeprecation() error {
	d := c.op.doc
	if d.Deprecation.IsZero() && d.Sunset.IsZero() {
		return nil
	}
	if !d.Deprecated {
		return fmt.Errorf("Doc.Deprecation or Doc.Sunset set but Doc.Deprecated is false")
	}
	p := &deprecationPlan{}
	check := func(field string, t time.Time) error {
		if t.Nanosecond() != 0 {
			return fmt.Errorf("Doc.%s %s is not a whole second", field, t.Format(time.RFC3339Nano))
		}
		if y := t.UTC().Year(); y < 1 || y > 9999 {
			return fmt.Errorf("Doc.%s %s is outside the years 1 to 9999", field, t.Format(time.RFC3339))
		}
		return nil
	}
	if t := d.Deprecation; !t.IsZero() {
		if err := check(deprecationHeader, t); err != nil {
			return err
		}
		p.deprecation = "@" + strconv.FormatInt(t.Unix(), 10)
	}
	if t := d.Sunset; !t.IsZero() {
		if err := check(sunsetHeader, t); err != nil {
			return err
		}
		if !d.Deprecation.IsZero() && t.Before(d.Deprecation) {
			return fmt.Errorf("Doc.Sunset %s is before Doc.Deprecation %s",
				t.UTC().Format(time.RFC3339), d.Deprecation.UTC().Format(time.RFC3339))
		}
		p.sunset = t.UTC().Format(http.TimeFormat)
	}
	for _, name := range p.names() {
		if where := c.declares(name); where != "" {
			return fmt.Errorf("%s declares the %s header, which Doc.%s sends", where, name, name)
		}
	}
	c.deprecated = p
	return nil
}

// names returns the headers p sends.
func (p *deprecationPlan) names() []string {
	var names []string
	if p.deprecation != "" {
		names = append(names, deprecationHeader)
	}
	if p.sunset != "" {
		names = append(names, sunsetHeader)
	}
	return names
}

// declares names what part of c declares the response header name (its
// output, a failure row, or a middleware), or returns "".
func (c *compiledOp) declares(name string) string {
	if c.out != nil {
		for _, h := range c.out.headers {
			if strings.EqualFold(h.name, name) {
				return "the output"
			}
		}
	}
	for i, pp := range c.rows {
		if pp == nil {
			continue
		}
		for _, h := range pp.headers() {
			if strings.EqualFold(h.name, name) {
				return fmt.Sprintf("failure row %d (%s)", i, c.op.doc.Failures[i].label)
			}
		}
	}
	for _, m := range c.chain {
		for _, h := range m.headers {
			if strings.EqualFold(h.name, name) {
				return fmt.Sprintf("middleware %s", m.name)
			}
		}
	}
	return ""
}

// set sets p's headers on h. A nil p does nothing.
func (p *deprecationPlan) set(h http.Header) {
	if p == nil {
		return
	}
	if p.deprecation != "" {
		h[deprecationHeader] = []string{p.deprecation}
	}
	if p.sunset != "" {
		h[sunsetHeader] = []string{p.sunset}
	}
}

// unset removes p's headers from h. A nil p does nothing.
func (p *deprecationPlan) unset(h http.Header) {
	if p == nil {
		return
	}
	for _, name := range p.names() {
		delete(h, name)
	}
}

// document adds p's headers to the response object r, as required except on
// a 101, which an [Upgrade.Accept] library may write without them.
func (p *deprecationPlan) document(r map[string]any, status int) {
	if p == nil {
		return
	}
	// Copy: a failure row's description may share r's map.
	held, _ := r["headers"].(map[string]any)
	headers := maps.Clone(held)
	if headers == nil {
		headers = map[string]any{}
	}
	r["headers"] = headers
	required := status != http.StatusSwitchingProtocols
	if p.deprecation != "" {
		headers[deprecationHeader] = map[string]any{
			"description": "The operation is deprecated as of this moment, which may be in the future: a structured-field Date, seconds since the epoch (RFC 9745)",
			"required":    required,
			"schema":      map[string]any{"type": "string", "const": p.deprecation},
		}
	}
	if p.sunset != "" {
		headers[sunsetHeader] = map[string]any{
			"description": "The operation is expected to stop answering at this moment: an HTTP-date (RFC 8594)",
			"required":    required,
			"schema":      map[string]any{"type": "string", "const": p.sunset},
		}
	}
}

// deprecate replaces the deprecation headers on the response with rt's, or
// removes them when rt is nil. ServeHTTP calls it before the root scope runs,
// and rematch again when a root middleware moves the request.
func (c *requestContext) deprecate(rt *route) {
	if c.header == nil {
		return
	}
	var p *deprecationPlan
	if rt != nil && rt.op != nil {
		p = rt.op.deprecated
	}
	if p == c.deprecated {
		return
	}
	c.deprecated.unset(c.header)
	p.set(c.header)
	c.deprecated = p
}
