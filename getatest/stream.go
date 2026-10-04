package getatest

import (
	"bufio"
	"io"
	"net/http"
	"strings"
)

// Event is one server-sent event.
type Event struct {
	Name string
	ID   string
	Data string
}

// EventStream reads an open event stream.
type EventStream struct {
	// Response is the response that opened the stream, or the refusal,
	// fully read, if the server did not stream.
	Response *Response
	body     io.ReadCloser
	r        *bufio.Reader
	// Comments counts the comment lines (keep-alives) read so far.
	Comments int
	// conforms checks one raw event against the document.
	conforms func(raw []byte)
}

// Stream sends GET path with Accept: text/event-stream, unless the client
// sets another Accept, and returns the stream. If the response is not an
// event stream, Response holds it, Next reports false, and the body is
// checked against the document as by [Client.Send].
func (c *Client) Stream(path string) *EventStream {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), http.MethodGet, c.srv.URL+path, nil)
	if err != nil {
		c.t.Fatalf("getatest: %v", err)
	}
	if c.hdr.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	c.addHeaders(req)
	id := c.log.number(req)
	res, err := c.hc.Do(req)
	sv := c.log.take(id) // on a failure too, so the record goes
	if err != nil {
		c.t.Fatalf("getatest: GET %s: %v", path, err)
	}
	c.documented(req, sv, res.StatusCode)
	s := &EventStream{Response: &Response{t: c.t, Status: res.StatusCode, Header: res.Header, opts: c.app.JSONOptions()}}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		defer res.Body.Close()
		s.Response.Body, _ = io.ReadAll(res.Body)
		c.conforms(req, sv, res, s.Response.Body)
		return s
	}
	s.body = res.Body
	s.r = bufio.NewReader(res.Body)
	// Check the headers now and each event as Next reads it.
	c.conforms(req, sv, res, nil)
	if sv.ok {
		s.conforms = func(raw []byte) {
			c.t.Helper()
			if err := c.app.Conforms(sv.match, res.StatusCode, nil, raw); err != nil {
				c.t.Errorf("getatest: GET %s%s: %v", req.URL.Path, servedAs(req, sv), err)
			}
		}
	}
	c.t.Cleanup(s.Close)
	return s
}

// Next reads the next event. It reports false when the stream has ended.
// An event whose data does not match the document's schema fails the test.
func (s *EventStream) Next() (Event, bool) {
	if s.r == nil {
		return Event{}, false
	}
	var e Event
	var data []string
	var raw []byte
	seen := false
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return Event{}, false
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			if seen {
				e.Data = strings.Join(data, "\n")
				if s.conforms != nil {
					s.conforms(append(raw, '\n'))
				}
				return e, true
			}
		case strings.HasPrefix(line, ":"):
			s.Comments++
		default:
			seen = true
			raw = append(append(raw, line...), '\n')
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				e.Name = value
			case "id":
				e.ID = value
			case "data":
				data = append(data, value)
			}
		}
	}
}

// Close closes the stream.
func (s *EventStream) Close() {
	if s.body != nil {
		s.body.Close()
	}
}

// Upgraded is the outcome of an upgrade request: either the connection
// switched, or the server refused with an ordinary response.
type Upgraded struct {
	// Switched reports whether the server answered 101.
	Switched bool
	// Response is the response; on a refusal its body is fully read.
	Response *Response
	// Conn is the switched connection, when Switched.
	Conn io.ReadWriteCloser
}

// Upgrade sends GET path asking to switch to protocol. Unless protocol is
// "", it sets Connection: Upgrade and Upgrade: protocol, replacing the
// client's values. A refusal's body is checked against the document as by
// [Client.Send].
func (c *Client) Upgrade(path, protocol string) *Upgraded {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), http.MethodGet, c.srv.URL+path, nil)
	if err != nil {
		c.t.Fatalf("getatest: %v", err)
	}
	if protocol != "" {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", protocol)
	}
	c.addHeaders(req)
	id := c.log.number(req)
	res, err := c.hc.Do(req)
	sv := c.log.take(id) // on a failure too, so the record goes
	if err != nil {
		c.t.Fatalf("getatest: upgrade %s: %v", path, err)
	}
	c.documented(req, sv, res.StatusCode)
	out := &Upgraded{Response: &Response{t: c.t, Status: res.StatusCode, Header: res.Header, opts: c.app.JSONOptions()}}
	if res.StatusCode == http.StatusSwitchingProtocols {
		conn, ok := res.Body.(io.ReadWriteCloser)
		if !ok {
			c.t.Fatalf("getatest: 101 response body is not writable")
		}
		out.Switched, out.Conn = true, conn
		c.t.Cleanup(func() { conn.Close() })
		c.conforms(req, sv, res, nil) // its headers
		return out
	}
	defer res.Body.Close()
	out.Response.Body, _ = io.ReadAll(res.Body)
	c.conforms(req, sv, res, out.Response.Body)
	return out
}
