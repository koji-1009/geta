package geta

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// OpenAPIVersion is a version of the OpenAPI Specification: [OpenAPI31], the
// default, or [OpenAPI32]. [New] refuses any other.
type OpenAPIVersion string

const (
	// OpenAPI31 is OpenAPI 3.1.0, the default.
	OpenAPI31 OpenAPIVersion = "3.1.0"
	// OpenAPI32 is OpenAPI 3.2.0. The document adds what 3.2 can state: an
	// event stream ([Stream]) described per event with itemSchema, a summary
	// on every response, the envelope's cookies in the Set-Cookie schema, and
	// a [Route.Query] operation as the path's query operation.
	OpenAPI32 OpenAPIVersion = "3.2.0"
)

// oasFeatures is what a version of the document can state beyond 3.1.
type oasFeatures struct {
	itemSchema       bool // itemSchema for sequential media (3.2 §4.14.3.1.1)
	responseSummary  bool // Response Object summary (3.2 §4.17.1)
	cookieSchema     bool // Set-Cookie per cookie, simple+explode (3.2 §4.21.3)
	queryOperation   bool // Path Item query operation (3.2 §4.10.1)
	securityScheme32 bool // oauth2MetadataUrl, deprecated, deviceAuthorization
}

// openAPIVersions are the versions geta renders, with their features.
var openAPIVersions = map[OpenAPIVersion]oasFeatures{
	OpenAPI31: {},
	OpenAPI32: {itemSchema: true, responseSummary: true, cookieSchema: true, queryOperation: true, securityScheme32: true},
}

// check31 refuses fields a 3.1 Security Scheme Object cannot hold, rather
// than silently dropping them.
func (s Scheme) check31() error {
	const use = "; use geta.OpenAPI32"
	switch {
	case s.OAuth2MetadataURL != "":
		return errors.New("OpenAPI 3.1 has no oauth2MetadataUrl" + use)
	case s.Deprecated:
		return errors.New("OpenAPI 3.1 has no deprecated security scheme" + use)
	case s.Flows != nil && s.Flows.DeviceAuthorization != nil:
		return errors.New("OpenAPI 3.1 has no deviceAuthorization flow" + use)
	}
	return nil
}

// WithOpenAPI selects the OpenAPI version of the document. The default is
// [OpenAPI31]. New refuses a version geta does not render.
func WithOpenAPI(v OpenAPIVersion) Option { return func(c *config) { c.oas = v } }

// openAPI renders the OpenAPI document. Maps are sorted by the encoder and
// lists follow declaration order, so the output is deterministic.
func (a *App) openAPI(info Info, reg *registry) ([]byte, error) {
	paths := map[string]any{}
	schemes := map[string]Scheme{}
	tags := map[string]bool{}
	addScheme := func(s Scheme, where string) error {
		if prev, ok := schemes[s.Name]; ok && !prev.equal(s) {
			return fmt.Errorf("%s: security scheme %q has two definitions: %+v and %+v", where, s.Name, prev, s)
		}
		if !a.features.securityScheme32 {
			if err := s.check31(); err != nil {
				return fmt.Errorf("%s: security scheme %q: %w", where, s.Name, err)
			}
		}
		schemes[s.Name] = s
		return nil
	}
	for _, op := range a.ops {
		where := op.method + " " + op.path
		item, _ := paths[op.path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[op.path] = item
		}
		item[strings.ToLower(op.method)] = op.document(&op.limits, nil)
		for _, t := range op.op.doc.Tags {
			tags[t] = true
		}
		for _, s := range op.security {
			if err := addScheme(s, where); err != nil {
				return nil, err
			}
		}
		for _, m := range op.chain {
			if m.gate != nil {
				for _, s := range m.gate.Default {
					if err := addScheme(s, where); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	named := map[string]*codec{}
	for _, c := range reg.codecs {
		if c.name == "" {
			continue
		}
		if c.name == "Problem" {
			return nil, fmt.Errorf("schema name %q is reserved; rename %s", c.name, qualified(c.t))
		}
		named[c.name] = c
	}
	under, err := readUnder(a.ops, paths, named)
	if err != nil {
		return nil, err
	}
	read, split := splitComponents(paths, named, under)
	if len(split) > 0 {
		// Re-render so requests refer to the split components' request schemas.
		for _, op := range a.ops {
			paths[op.path].(map[string]any)[strings.ToLower(op.method)] = op.document(&op.limits, split)
		}
	}
	// A component a request reads states the backstops; one only responses
	// write states none. One both read and written whose schemas differ is
	// split: the response's under the plain name, the request's with
	// inputSuffix.
	schemas := map[string]any{"Problem": problemSchema()}
	for name, c := range named {
		switch {
		case split[name]:
			schemas[name] = c.schema.document(nil)
			schemas[name+inputSuffix] = c.schema.render(under[name], split)
		case read[name]:
			schemas[name] = c.schema.render(under[name], split)
		default:
			schemas[name] = c.schema.document(nil)
		}
	}
	// Drop unreferenced components. A row's description (OnAsProblem) is
	// inlined, so its type is a component only if something else refers to it.
	used := map[string]bool{"Problem": true}
	refs(paths, used)
	for todo := slices.Collect(maps.Keys(used)); len(todo) > 0; {
		name := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		found := map[string]bool{}
		refs(schemas[name], found)
		for n := range found {
			if !used[n] {
				used[n] = true
				todo = append(todo, n)
			}
		}
	}
	maps.DeleteFunc(schemas, func(name string, _ any) bool { return !used[name] })
	components := map[string]any{"schemas": schemas}
	if len(schemes) > 0 {
		ss := map[string]any{}
		for name, s := range schemes {
			ss[name] = schemeDocument(s)
		}
		components["securitySchemes"] = ss
	}
	infoDoc := map[string]any{"title": info.Title, "version": info.Version}
	if info.Description != "" {
		infoDoc["description"] = info.Description
	}
	doc := map[string]any{
		"openapi":    string(a.oas),
		"info":       infoDoc,
		"paths":      paths,
		"components": components,
	}
	if len(tags) > 0 {
		var list []any
		for _, t := range slices.Sorted(maps.Keys(tags)) {
			list = append(list, map[string]any{"name": t})
		}
		doc["tags"] = list
	}
	b, err := json.Marshal(doc, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		// The only possible error is text that is not UTF-8, from a doc tag,
		// a schema tag, WithInfo, or a middleware declaration. Doc texts are
		// refused earlier, in compile.
		var at jsontext.Pointer
		if se, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
			at = se.JSONPointer
		}
		return nil, fmt.Errorf("OpenAPI document text at %s is not UTF-8: %w", at, err)
	}
	return append(b, '\n'), nil
}

// edgesOf returns, for each named component, the components it refers to.
func edgesOf(named map[string]*codec) map[string]map[string]bool {
	edges := map[string]map[string]bool{}
	for name, c := range named {
		edges[name] = map[string]bool{}
		refs(c.schema.document(nil), edges[name])
	}
	return edges
}

// readUnder returns, for each component a request reads, the limits of the
// operations that read it. Two operations whose limits (Doc.Limits) would
// render the component differently are an error; limits that differ only
// where the component states nothing are fine.
func readUnder(ops []*compiledOp, paths map[string]any, named map[string]*codec) (map[string]*Limits, error) {
	edges := edgesOf(named)
	under := map[string]*Limits{}
	by := map[string]*compiledOp{}
	var errs []error
	for _, op := range ops {
		d := paths[op.path].(map[string]any)[strings.ToLower(op.method)].(map[string]any)
		read := map[string]bool{}
		refs(d["parameters"], read)
		refs(d["requestBody"], read)
		reach(read, edges)
		// Every name here is a named codec: Problem appears only in responses.
		for _, name := range slices.Sorted(maps.Keys(read)) {
			c := named[name]
			prev, ok := under[name]
			if !ok {
				under[name], by[name] = &op.limits, op
				continue
			}
			if *prev != op.limits && !reflect.DeepEqual(c.schema.document(prev), c.schema.document(&op.limits)) {
				first := by[name]
				errs = append(errs, fmt.Errorf("%s %s and %s %s read %s under conflicting limits (MaxStringLength %d and %d, MaxItems %d and %d)",
					first.method, first.path, op.method, op.path, c.t,
					prev.MaxStringLength, op.limits.MaxStringLength, prev.MaxItems, op.limits.MaxItems))
			}
		}
	}
	return under, errors.Join(errs...)
}

// splitComponents returns the components a request reads, directly or
// transitively (read), and of those, the ones a response also writes whose
// request schema under its limits differs from the response schema (split).
func splitComponents(paths map[string]any, named map[string]*codec, under map[string]*Limits) (read, split map[string]bool) {
	read, written := map[string]bool{}, map[string]bool{}
	for _, item := range paths {
		for _, op := range item.(map[string]any) {
			op := op.(map[string]any)
			refs(op["parameters"], read)
			refs(op["requestBody"], read)
			refs(op["responses"], written)
		}
	}
	edges := edgesOf(named)
	states := map[string]bool{} // a component that states a backstop itself
	for name, c := range named {
		if lim := under[name]; lim != nil {
			states[name] = !reflect.DeepEqual(c.schema.document(lim), c.schema.document(nil))
		}
	}
	reach(read, edges)
	reach(written, edges)
	split = map[string]bool{}
	for name := range read {
		if !written[name] {
			continue
		}
		below := map[string]bool{name: true}
		reach(below, edges)
		for n := range below {
			if states[n] {
				split[name] = true
				break
			}
		}
	}
	return read, split
}

// refs adds to names the components v, a rendered part of the document,
// refers to.
func refs(v any, names map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		if r, ok := v["$ref"].(string); ok {
			if name, ok := strings.CutPrefix(r, componentRef); ok {
				names[name] = true
			}
		}
		for _, x := range v {
			refs(x, names)
		}
	case []any:
		for _, x := range v {
			refs(x, names)
		}
	}
}

// reach adds to names every component reachable from them by edges.
func reach(names map[string]bool, edges map[string]map[string]bool) {
	stack := slices.Collect(maps.Keys(names))
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for m := range edges[n] {
			if !names[m] {
				names[m] = true
				stack = append(stack, m)
			}
		}
	}
}

func schemeDocument(s Scheme) map[string]any {
	m := map[string]any{"type": s.Type}
	put := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	put("scheme", s.Scheme)
	put("bearerFormat", s.BearerFormat)
	put("in", s.In)
	put("name", s.Param)
	put("openIdConnectUrl", s.OpenIDConnectURL)
	put("oauth2MetadataUrl", s.OAuth2MetadataURL)
	put("description", s.Description)
	if s.Deprecated {
		m["deprecated"] = true
	}
	if s.Flows != nil {
		flows := map[string]any{}
		for _, fl := range s.Flows.list() {
			f := fl.f
			if f == nil {
				continue
			}
			// scopes is required, even when empty.
			fd := map[string]any{"scopes": map[string]string{}}
			if f.Scopes != nil {
				fd["scopes"] = f.Scopes
			}
			for k, v := range map[string]string{"authorizationUrl": f.AuthorizationURL, "deviceAuthorizationUrl": f.DeviceAuthorizationURL,
				"tokenUrl": f.TokenURL, "refreshUrl": f.RefreshURL} {
				if v != "" {
					fd[k] = v
				}
			}
			flows[fl.name] = fd
		}
		m["flows"] = flows
	}
	return m
}

// document returns the operation object. Request schemas state the
// backstops lim and refer to split components by their request names;
// response schemas state no backstop.
func (c *compiledOp) document(lim *Limits, split map[string]bool) map[string]any {
	d := c.op.doc
	m := map[string]any{"operationId": c.opID}
	if d.Summary != "" {
		m["summary"] = d.Summary
	}
	if d.Description != "" {
		m["description"] = d.Description
	}
	if len(d.Tags) > 0 {
		m["tags"] = d.Tags
	}
	if d.Deprecated {
		m["deprecated"] = true
	}
	var params []any
	if c.options {
		// geta's OPTIONS binds nothing: any non-empty segment matches.
		names, _ := parsePath(c.path)
		for _, name := range names {
			params = append(params, map[string]any{
				"name":     name,
				"in":       "path",
				"required": true,
				"schema":   map[string]any{"type": "string", "minLength": 1},
			})
		}
	}
	for _, p := range c.in.params {
		param := map[string]any{
			"name": p.name,
			"in":   p.in,
			// geta fills a missing parameter that has a default.
			"required": !p.optional && p.use.Default == nil,
			"schema":   p.use.render(lim, split),
		}
		if p.joined {
			// A precondition field is held to no backstop (conditionField).
			param["schema"] = p.use.render(nil, split)
		}
		if p.desc != "" {
			param["description"] = p.desc
		}
		if p.use.Deprecated {
			param["deprecated"] = true
		}
		if p.deep != nil {
			// name[member]=value per member, closed as a form is.
			param["style"], param["explode"] = "deepObject", true
			param["schema"] = p.deep.document(lim, split)
		}
		params = append(params, param)
	}
	if len(params) > 0 {
		m["parameters"] = params
	}
	if b := c.in.body; b != nil {
		m["requestBody"] = map[string]any{
			"required": !b.optional,
			"content":  map[string]any{"application/json": map[string]any{"schema": b.use.render(lim, split)}},
		}
		if b.desc != "" {
			m["requestBody"].(map[string]any)["description"] = b.desc
		}
	}
	if f := c.in.form; f != nil {
		m["requestBody"] = map[string]any{
			"required": !f.optional,
			"content":  map[string]any{f.mediaType(): map[string]any{"schema": f.document(lim, split)}},
		}
		if f.desc != "" {
			m["requestBody"].(map[string]any)["description"] = f.desc
		}
	}
	if rp := c.in.raw; rp != nil {
		// Raw content has a media type and no schema (OpenAPI 3.1 §4.8.14.4).
		m["requestBody"] = map[string]any{
			"required": !rp.optional,
			"content":  map[string]any{rp.mediaType: map[string]any{}},
		}
		if rp.desc != "" {
			m["requestBody"].(map[string]any)["description"] = rp.desc
		}
	}
	m["responses"] = c.responses()
	// One requirement object per alternative, holding one scheme per gate
	// with the scopes the chain requires (Middleware.Scopes). Alternatives a
	// middleware refuses are already left out (planScopes).
	sec := []any{}
	for _, req := range c.documented {
		obj := map[string]any{}
		for _, s := range req {
			obj[s.Name] = append([]string{}, c.scopes[s.Name]...)
		}
		sec = append(sec, obj)
	}
	m["security"] = sec
	return m
}

// responses lists every status the operation can answer: its successes, its
// failure rows, and what geta and its middleware answer on their own.
func (c *compiledOp) responses() map[string]any {
	reasons := map[int][]string{}
	plain := map[int]int{} // the causes of each status that answer a plain problem
	for i, f := range c.op.doc.Failures {
		reason := f.detail
		if reason == "" {
			reason = statusText(f.status)
		}
		if f.typ != "" {
			reason += " (type " + f.typ + ")"
		}
		if c.rows[i] == nil {
			plain[f.status]++
		}
		if !slices.Contains(reasons[f.status], reason) {
			reasons[f.status] = append(reasons[f.status], reason)
		}
	}
	// Causes from geta and middleware are listed after the rows' reasons,
	// even on the same status.
	add := func(status int, reason string) {
		if !slices.Contains(reasons[status], reason) {
			reasons[status] = append(reasons[status], reason)
			plain[status]++
		}
	}
	body := c.in.body != nil || c.in.form != nil || c.in.raw != nil
	if len(c.in.params) > 0 || body {
		add(http.StatusBadRequest, "The request does not match its contract")
	}
	if body {
		add(http.StatusRequestEntityTooLarge, fmt.Sprintf("The request body is larger than %d bytes (Limits.MaxBodyBytes)", c.limits.MaxBodyBytes))
		add(http.StatusUnsupportedMediaType, "The request has content of a media type the operation does not take, or no Content-Type, or content in a content coding (Content-Encoding)")
	} else {
		// contentRefused: content sent without a declared body is refused.
		add(http.StatusUnsupportedMediaType, "The request has content, which the operation does not take")
	}
	var mwHeaders []mwHeader // what the middleware send with what they answer
	for _, m := range c.chain {
		if m.gate != nil && len(c.security) == 0 {
			continue
		}
		for _, an := range m.answers {
			if an.getOnly && (c.method != http.MethodGet || !slices.Contains(c.successes(), http.StatusOK)) {
				continue
			}
			if an.bodyOnly && !body {
				continue
			}
			add(an.status, an.reason)
		}
		mwHeaders = append(mwHeaders, m.headers...)
	}
	// Conditional's Check: 412 on any method, 304 on GET (and HEAD), 428 when
	// required.
	if c.in.cond != nil {
		if c.method == http.MethodGet {
			add(http.StatusNotModified, "The representation has not changed")
			add(http.StatusPreconditionFailed, "A precondition failed: If-Match, or If-Unmodified-Since")
		} else {
			add(http.StatusPreconditionFailed, "A precondition failed: If-Match, If-Unmodified-Since, or If-None-Match")
		}
		if c.in.required {
			add(http.StatusPreconditionRequired, "The request carries no precondition, and this operation requires one")
		}
	}
	if sp, ok := c.out.special.(interface{ answers() []answer }); ok && c.out.kind == outSpecial {
		for _, an := range sp.answers() {
			add(an.status, an.reason)
		}
	}
	if c.options {
		// OPTIONS runs no handler, so it has no handler's 504.
		add(http.StatusInternalServerError, "A defect in the server, such as a root middleware that replaced the request context, or a panic")
	} else {
		add(http.StatusInternalServerError, "A defect in the server, such as an error no failure row matches, output that cannot be encoded, or a panic")
		// A handler's context.DeadlineExceeded is 504 whatever set the
		// deadline.
		add(http.StatusGatewayTimeout, "The deadline passed before a response")
	}

	described := c.failureResponses(plain)
	c.problems = described // for Conforms
	// The typed response headers Conforms checks.
	c.headers = map[int][]responseHeader{}
	// A middleware's headers are optional on the status it answers. A header
	// already stated by a row, the output, geta, or a typed middleware
	// declaration wins; typed declarations go before untyped ones.
	var typedFirst []mwHeader
	for _, h := range mwHeaders {
		if h.schema != nil {
			typedFirst = append(typedFirst, h)
		}
	}
	for _, h := range mwHeaders {
		if h.schema == nil {
			typedFirst = append(typedFirst, h)
		}
	}
	middlewareHeaders := func(r map[string]any, status int) {
		for _, h := range typedFirst {
			if h.status != status {
				continue
			}
			headers, _ := r["headers"].(map[string]any)
			if headers == nil {
				headers = map[string]any{}
				r["headers"] = headers
			}
			if _, has := headers[headerKey(headers, h.name)]; has {
				continue
			}
			schema := map[string]any{"type": "string"}
			if h.schema != nil {
				schema = h.schema.document(nil)
			}
			header := map[string]any{"required": false, "schema": schema}
			if h.description != "" {
				header["description"] = h.description
			}
			headers[h.name] = header
			if h.c != nil {
				c.headers[status] = append(c.headers[status], responseHeader{name: h.name, c: h.c, use: h.schema})
			}
		}
	}
	successes := c.successes()
	out := map[string]any{}
	for _, status := range slices.Sorted(maps.Keys(reasons)) {
		if slices.Contains(successes, status) {
			continue // stated below, with what the operation answers
		}
		r := map[string]any{"description": strings.Join(reasons[status], "; ")}
		if c.app.features.responseSummary {
			r["summary"] = statusText(status)
		}
		fr := described[status]
		switch {
		case fr != nil:
			// Rows that describe their problem.
			r["content"] = fr.content()
			if len(fr.headers) > 0 {
				r["headers"] = fr.headers
			}
			for _, name := range slices.Sorted(maps.Keys(fr.outs)) {
				h := fr.outs[name]
				required, _ := fr.headers[name].(map[string]any)["required"].(bool)
				c.headers[status] = append(c.headers[status], responseHeader{name: name, c: h.c, use: h.use, required: required})
			}
		case status >= 400 && !bodyless(status):
			// A non-success 2xx or 3xx comes from middleware and has no
			// problem content.
			r["content"] = map[string]any{
				ProblemContentType: map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Problem"}},
			}
		}
		if status == http.StatusUnsupportedMediaType && body {
			headers, _ := r["headers"].(map[string]any)
			if headers == nil {
				headers = map[string]any{}
			}
			// geta's own headers replace a row's of the same name.
			accept := c.acceptHeaders()
			for name := range accept {
				delete(headers, headerKey(headers, name))
			}
			maps.Copy(headers, accept)
			r["headers"] = headers
			c.headers[status] = slices.DeleteFunc(c.headers[status], func(h responseHeader) bool {
				_, own := accept[headerKey(accept, h.name)]
				return own
			})
		}
		middlewareHeaders(r, status)
		c.deprecated.document(r, status)
		out[strconv.Itoa(status)] = r
	}
	for _, s := range successes {
		r := c.success(s)
		if len(reasons[s]) > 0 {
			// A middleware answering a success status (a cache) is listed after.
			r["description"] = strings.Join(append([]string{statusText(s)}, reasons[s]...), "; ")
		}
		if c.out != nil && c.out.kind == outEnvelope && !c.options {
			for _, h := range c.out.headers {
				c.headers[s] = append(c.headers[s], responseHeader{name: h.name, c: h.c, use: h.use, required: !h.optional})
			}
		}
		middlewareHeaders(r, s)
		out[strconv.Itoa(s)] = r
	}
	return out
}

// bodyMediaType returns the media type of the operation's body, or "" if it
// takes none.
func (c *compiledOp) bodyMediaType() string {
	switch {
	case c.in.body != nil:
		return bodyMediaTypes["json"]
	case c.in.form != nil:
		return c.in.form.mediaType()
	case c.in.raw != nil:
		return c.in.raw.mediaType
	}
	return ""
}

// acceptHeaders returns the headers geta's 415s carry: Accept (plus
// Accept-Patch or Accept-Query, per acceptFor) for an unsupported media
// type, or Accept-Encoding: identity for a content coding. They are not
// required, since a row or middleware may answer 415 without them.
func (c *compiledOp) acceptHeaders() map[string]any {
	mt := c.bodyMediaType()
	h := map[string]any{
		"Accept": map[string]any{
			"description": "The media type the operation takes, sent with geta's own 415 for content of another media type (RFC 9110 section 15.5.16)",
			"schema":      map[string]any{"type": "string", "enum": []string{mt}},
		},
		"Accept-Encoding": map[string]any{
			"description": "identity: geta decodes no content coding; sent with geta's own 415 for content in one, and only with that 415 (RFC 9110 section 12.5.3)",
			"schema":      map[string]any{"type": "string", "enum": []string{"identity"}},
		},
	}
	switch name := acceptFor(c.method); name {
	case "Accept-Patch":
		h[name] = map[string]any{
			"description": "The media type the operation takes, sent with geta's own 415 for content of another media type (RFC 5789 section 2.2)",
			"schema":      map[string]any{"type": "string", "enum": []string{mt}},
		}
	case "Accept-Query":
		h[name] = map[string]any{
			"description": "The media type the operation takes as a query, sent with geta's own 415 for content of another media type (draft-ietf-httpbis-safe-method-w-body section 3)",
			"schema":      map[string]any{"type": "string", "enum": []string{mt}},
		}
	}
	return h
}

// statusText returns the reason phrase, or "Status <code>" if there is none.
func statusText(status int) string {
	if t := http.StatusText(status); t != "" {
		return t
	}
	return "Status " + strconv.Itoa(status)
}

// success returns the success response. Its schemas state no backstop. The
// body is checked against them only in tests (Conforms). An output header is
// checked at run time: a value no header can carry as written is a 500.
func (c *compiledOp) success(status int) map[string]any {
	r := map[string]any{"description": statusText(status)}
	if c.app.features.responseSummary {
		r["summary"] = statusText(status)
	}
	if c.options {
		// One value unless another template shares some of these paths.
		schema := map[string]any{"type": "string"}
		if len(c.allow) == 1 {
			schema["const"] = c.allow[0]
		} else if len(c.allow) > 1 {
			schema["enum"] = c.allow
		}
		r["headers"] = map[string]any{"Allow": map[string]any{
			"description": "The methods this URL serves",
			"required":    true,
			"schema":      schema,
		}}
		return r
	}
	p := c.out
	switch p.kind {
	case outJSON:
		r["content"] = map[string]any{"application/json": map[string]any{"schema": p.bodyUse.document(nil)}}
	case outEnvelope:
		if p.body != nil {
			r["content"] = map[string]any{"application/json": map[string]any{"schema": p.bodyUse.document(nil)}}
		}
		if p.raw != nil {
			r["content"] = map[string]any{p.raw.mediaType: map[string]any{}}
		}
		headers := map[string]any{}
		for _, h := range p.headers {
			header := map[string]any{"required": !h.optional, "schema": h.use.document(nil)}
			if h.desc != "" {
				header["description"] = h.desc
			}
			headers[h.name] = header
		}
		if len(p.cookies) > 0 {
			names := make([]string, len(p.cookies))
			for i, ck := range p.cookies {
				names[i] = ck.name
			}
			setCookie := map[string]any{
				"description": "Sets " + strings.Join(names, ", "),
				"schema":      map[string]any{"type": "string"},
			}
			if c.app.features.cookieSchema {
				// One optional Set-Cookie line per cookie; middleware may set
				// others.
				props := map[string]any{}
				for _, n := range names {
					props[n] = map[string]any{"type": "string"}
				}
				setCookie["schema"] = map[string]any{"type": "object", "properties": props}
				setCookie["style"] = "simple"
				setCookie["explode"] = true
			}
			headers["Set-Cookie"] = setCookie
		}
		if len(headers) > 0 {
			r["headers"] = headers
		}
	case outSpecial:
		if sp, ok := p.special.(interface {
			document(map[string]any, oasFeatures)
		}); ok {
			sp.document(r, c.app.features)
		}
	}
	c.deprecated.document(r, status)
	return r
}
