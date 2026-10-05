// Package getavet checks a geta application's declarations before it runs.
// It reports, from source, the mistakes geta.New would refuse at startup.
// Each check calls the geta.Check* function geta.New applies, so every
// diagnostic is geta.New's text.
//
// getavet reports:
//
//   - an input that binds a path parameter its URL does not have, and a URL
//     parameter the input does not bind. The URL is the package directory's,
//     by the rule geta sync uses; outside a route tree, path parameters are
//     not checked.
//   - a schema tag that is malformed, does not apply to its field's type,
//     names an unknown format or a format on a type that carries its own,
//     declares bounds no value meets, or has a default, example, or enum
//     member the schema refuses.
//   - an input field geta.New refuses (geta.CheckInputField): location and
//     body tags, raw and form bodies, parameter names and types, deepObject
//     query parameters and their keys, embedded structs, and defaults on a
//     pointer, path parameter, or body.
//   - a form or multipart body field geta.New refuses (geta.CheckFormField):
//     tags, names, types, geta.File fields and their schema tags.
//   - an output envelope field geta.New refuses (geta.CheckEnvelopeField):
//     header, cookie, body, and status fields, and embedded structs.
//   - a type geta.New derives no JSON form for, used as a body, parameter,
//     header, stream event (geta.Stream), or geta.OnAsProblem result: an
//     invalid component name, json tag options and pointers without
//     omitzero, conflicting or missing members, unsupported kinds such as
//     chan, func, array, complex, and time.Duration, a type with only half
//     of the text marshaling methods, a misused geta.FormatType or
//     geta.File, and nested geta.Nullable.
//   - a body on an operation a geta.Route literal builds in place under Get
//     or Delete (geta.CheckMethodBody).
//   - a constant success status geta does not accept or the output's status
//     field does not declare, and a negative constant geta.Doc Timeout.
//
// A maxLength, minLength, or enum member past the pattern-validation ceiling
// beside a pattern is reported only where a request reads the value
// (geta.CheckRequestSchemaTag); a type only a response writes has no
// ceiling.
//
// getavet leaves to geta.New what it cannot decide from source:
//
//   - anything that depends on options of the program's geta.New call:
//     sealed interfaces (geta.WithUnion), declared schemas and schema
//     keywords on types with their own JSON methods (geta.WithSchema), a
//     Route's Query operation (geta.WithOpenAPI), and lower bounds only
//     geta.Limits make unreachable (geta.WithLimits), even past
//     geta.DefaultLimits;
//   - two types taking one schema name across the program;
//   - the format a geta.FormatType returns at run time;
//   - an operation built elsewhere and placed in a geta.Route by name.
//
// Usage:
//
//	go run github.com/koji-1009/geta/getavet/cmd/getavet ./...
package getavet

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/internal/tree"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/types/typeutil"
)

const getaPath = "github.com/koji-1009/geta"

// Analyzer reports geta declaration mistakes.
var Analyzer = &analysis.Analyzer{
	Name:     "getavet",
	Doc:      "check geta path bindings and schema tags before the program runs",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	url, inTree := routeURL(pass)
	v := &vet{pass: pass, reported: map[string]bool{}}
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil), (*ast.CompositeLit)(nil)}, func(n ast.Node) {
		if lit, isLit := n.(*ast.CompositeLit); isLit {
			v.route(lit)
			v.docTimeout(lit)
			return
		}
		call := n.(*ast.CallExpr)
		if p, ok := problemType(pass, call); ok {
			v.problem(call, p)
			return
		}
		in, out, ok := operationTypes(pass, call)
		if !ok {
			return
		}
		paths := v.input(call, in, map[string]string{}, new(typeutil.Map))
		if out != nil {
			v.output(call, out)
		}
		v.status(call, out)
		if !inTree {
			return
		}
		params := urlParams(url)
		for _, p := range paths {
			if !slices.Contains(params, p.name) {
				v.report(call, p.pos, fmt.Sprintf("input binds path parameter %q not in the URL", p.name))
			}
		}
		for _, name := range params {
			if !slices.ContainsFunc(paths, func(p pathField) bool { return p.name == name }) {
				v.report(call, call.Pos(), fmt.Sprintf("input does not bind path parameter {%s}", name))
			}
		}
	})
	return nil, nil
}

// operationTypes recognises a call of geta.Op or geta.OpNoBody and returns
// its In and Out (nil for OpNoBody).
func operationTypes(pass *analysis.Pass, call *ast.CallExpr) (in, out types.Type, ok bool) {
	fun := ast.Unparen(call.Fun)
	if ix, isIndex := fun.(*ast.IndexListExpr); isIndex {
		fun = ix.X
	} else if ix, isIndex := fun.(*ast.IndexExpr); isIndex {
		fun = ix.X
	}
	var id *ast.Ident
	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return nil, nil, false
	}
	fn, isFunc := pass.TypesInfo.Uses[id].(*types.Func)
	if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != getaPath || (fn.Name() != "Op" && fn.Name() != "OpNoBody") {
		return nil, nil, false
	}
	// The package type-checks, so the instance is recorded.
	inst := pass.TypesInfo.Instances[id]
	in = inst.TypeArgs.At(0)
	if fn.Name() == "Op" {
		out = inst.TypeArgs.At(1)
	}
	return in, out, true
}

// routeMethods are the geta.Route fields by the method each answers.
var routeMethods = map[string]string{
	"Get": "GET", "Post": "POST", "Put": "PUT", "Patch": "PATCH", "Delete": "DELETE", "Query": geta.MethodQuery,
}

// route checks each operation a geta.Route literal builds in place by
// geta.CheckMethodBody. An operation placed by name is left to geta.New.
func (v *vet) route(lit *ast.CompositeLit) {
	tv, ok := v.pass.TypesInfo.Types[lit]
	if !ok || qualifiedName(tv.Type) != getaPath+".Route" {
		return
	}
	for i, elt := range lit.Elts {
		name, value, isField := element(tv.Type, i, elt)
		call, isCall := ast.Unparen(value).(*ast.CallExpr)
		if !isField || !isCall {
			continue
		}
		in, _, isOp := operationTypes(v.pass, call)
		if !isOp || !hasBody(in) {
			continue
		}
		if err := geta.CheckMethodBody(routeMethods[name]); err != nil {
			v.report(call, call.Pos(), err.Error())
		}
	}
}

// element returns the field name and value of element i of a literal of
// struct type t, keyed or positional.
func element(t types.Type, i int, elt ast.Expr) (name string, value ast.Expr, ok bool) {
	if kv, isKV := elt.(*ast.KeyValueExpr); isKV {
		key, isIdent := kv.Key.(*ast.Ident) // String is "<nil>" when it is none
		return key.String(), kv.Value, isIdent
	}
	return t.Underlying().(*types.Struct).Field(i).Name(), elt, true
}

// docTimeout checks a constant Timeout in a geta.Doc literal by
// geta.CheckDocTimeout. Whether the chain has a geta.Timeout is left to
// geta.New.
func (v *vet) docTimeout(lit *ast.CompositeLit) {
	tv, ok := v.pass.TypesInfo.Types[lit]
	if !ok || qualifiedName(tv.Type) != getaPath+".Doc" {
		return
	}
	for i, elt := range lit.Elts {
		name, value, isField := element(tv.Type, i, elt)
		if !isField || name != "Timeout" {
			continue
		}
		val := v.pass.TypesInfo.Types[value].Value
		if val == nil {
			continue
		}
		// A constant time.Duration fits in an int64.
		n, _ := constant.Int64Val(constant.ToInt(val))
		if err := geta.CheckDocTimeout(time.Duration(n)); err != nil {
			msg := err.Error()
			key := v.pass.Fset.Position(elt.Pos()).String() + msg
			if !v.reported[key] {
				v.reported[key] = true
				v.pass.Reportf(elt.Pos(), "%s", msg)
			}
		}
	}
}

// hasBody reports whether an input type has a body field, in itself or a
// struct it embeds untagged, as geta.New finds one.
func hasBody(t types.Type) bool {
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := range st.NumFields() {
		f, tag := st.Field(i), reflect.StructTag(st.Tag(i))
		if _, has := tag.Lookup("body"); has {
			return true
		}
		if f.Embedded() && tag == "" && hasBody(f.Type()) {
			return true
		}
	}
	return false
}

// problemType recognises a call of geta.OnAsProblem and returns its type
// argument P.
func problemType(pass *analysis.Pass, call *ast.CallExpr) (types.Type, bool) {
	fun := ast.Unparen(call.Fun)
	if ix, isIndex := fun.(*ast.IndexListExpr); isIndex {
		fun = ix.X
	} else if ix, isIndex := fun.(*ast.IndexExpr); isIndex {
		fun = ix.X
	}
	var id *ast.Ident
	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return nil, false
	}
	fn, isFunc := pass.TypesInfo.Uses[id].(*types.Func)
	if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != getaPath || fn.Name() != "OnAsProblem" {
		return nil, false
	}
	return pass.TypesInfo.Instances[id].TypeArgs.At(1), true
}

// problem checks P, the result of a geta.OnAsProblem row: a struct of
// extension members, or an envelope whose body is one.
func (v *vet) problem(call *ast.CallExpr, p types.Type) {
	if _, generic := p.(*types.TypeParam); generic {
		return
	}
	st, ok := p.Underlying().(*types.Struct)
	if err := geta.CheckProblemType(typeString(p), ok); err != nil {
		v.report(call, call.Pos(), err.Error())
		return
	}
	done := new(typeutil.Map)
	if !isEnvelope(st) {
		v.jsonType(call, call.Pos(), "", p, done, false)
		v.problemMembers(call, st)
		return
	}
	seen := map[string]bool{}
	var walk func(st *types.Struct)
	walk = func(st *types.Struct) {
		for i := range st.NumFields() {
			f, tag := st.Field(i), reflect.StructTag(st.Tag(i))
			vf := field(f, tag)
			if err := geta.CheckEnvelopeField(vf, seen); err != nil {
				v.report(call, f.Pos(), err.Error())
				continue
			}
			if geta.EnvelopeEmbedded(vf) {
				walk(f.Type().Underlying().(*types.Struct))
				continue
			}
			if err := geta.CheckProblemField(vf); err != nil {
				v.report(call, f.Pos(), err.Error())
				continue
			}
			if !f.Exported() {
				continue
			}
			if _, header := tag.Lookup("header"); header {
				v.jsonType(call, f.Pos(), f.Name(), deref(f.Type()), done, false)
			}
			if _, body := tag.Lookup("body"); body && !vf.Pointer {
				bst, isStruct := f.Type().Underlying().(*types.Struct)
				if err := geta.CheckProblemType(typeString(f.Type()), isStruct); err != nil {
					v.report(call, f.Pos(), err.Error())
					continue
				}
				v.jsonType(call, f.Pos(), f.Name(), f.Type(), done, false)
				v.problemMembers(call, bst)
			}
		}
	}
	walk(st)
}

// problemMembers checks a problem's extension members by
// geta.CheckProblemMember.
func (v *vet) problemMembers(call *ast.CallExpr, st *types.Struct) {
	names := map[string]bool{}
	var walk func(st *types.Struct)
	walk = func(st *types.Struct) {
		for i := range st.NumFields() {
			f, tag := st.Field(i), reflect.StructTag(st.Tag(i))
			vf := field(f, tag)
			member, embedded, err := geta.CheckMemberField(vf, names)
			if err != nil {
				continue // jsonType names it
			}
			if embedded {
				walk(f.Type().Underlying().(*types.Struct))
				continue
			}
			if member == "" {
				continue
			}
			if err := geta.CheckProblemMember(member, vf.Kind); err != nil {
				v.report(call, f.Pos(), err.Error())
			}
		}
	}
	walk(st)
}

type pathField struct {
	name string
	pos  token.Pos
}

type vet struct {
	pass     *analysis.Pass
	reported map[string]bool
	// elems holds, as geta.New prefixes an element's refusal, the slices
	// and maps jsonType is inside since the field it reports at ("T: ").
	elems []string
}

// report puts a diagnostic on the field when it is declared in this
// package, and otherwise on the call, naming where the field is.
func (v *vet) report(call *ast.CallExpr, at token.Pos, msg string) {
	pos := at
	if !v.inPackage(at) {
		pos = call.Pos()
		msg = v.pass.Fset.Position(at).String() + ": " + msg
	}
	key := v.pass.Fset.Position(pos).String() + msg
	if v.reported[key] {
		return
	}
	v.reported[key] = true
	v.pass.Reportf(pos, "%s", msg)
}

func (v *vet) inPackage(pos token.Pos) bool {
	for _, f := range v.pass.Files {
		if f.FileStart <= pos && pos <= f.FileEnd {
			return true
		}
	}
	return false
}

var locations = []string{"path", "query", "header", "cookie", "body"}

// input checks an input struct's fields, schema tags, and types, and
// returns its path fields. seen collects the names bound across embedded
// structs; done holds the types already checked.
func (v *vet) input(call *ast.CallExpr, in types.Type, seen map[string]string, done *typeutil.Map) []pathField {
	if _, generic := in.(*types.TypeParam); generic {
		return nil // judged where the operation is instantiated
	}
	st, ok := in.Underlying().(*types.Struct)
	if err := geta.CheckInputType(typeString(in), ok); err != nil {
		v.report(call, call.Pos(), err.Error())
		return nil
	}
	var paths []pathField
	for i := range st.NumFields() {
		f, tag := st.Field(i), reflect.StructTag(st.Tag(i))
		loc, name := "", ""
		for _, l := range locations {
			if n, has := tag.Lookup(l); has {
				loc, name = l, n
			}
		}
		vf := field(f, tag)
		if loc == "body" {
			vf.Kind = formKind(f.Type()) // a form body's is judged: a struct
		}
		walk, err := geta.CheckInputField(vf, seen)
		if err != nil {
			v.report(call, f.Pos(), err.Error())
		}
		// An embedded struct refused for its own schema tag still has
		// fields to check.
		if walk || (loc == "" && vf.Embedded && vf.Struct && !vf.Text) {
			paths = append(paths, v.input(call, f.Type(), seen, done)...)
			continue
		}
		if loc == "" {
			continue
		}
		if loc == "body" && name != "json" {
			// A form body's fields carry their own tags.
			if err == nil && vf.Kind == "struct" {
				v.form(call, deref(f.Type()).Underlying().(*types.Struct), name, map[string]string{}, done)
			}
			continue
		}
		v.schemaTag(call, f, tag, true)
		if loc == "path" {
			paths = append(paths, pathField{name, f.Pos()})
		}
		if loc == "query" && vf.Kind == "struct" {
			// A deepObject's fields are bound as a form's, not as JSON.
			if err == nil {
				v.form(call, deref(f.Type()).Underlying().(*types.Struct), deepObject, map[string]string{}, done)
			}
			continue
		}
		if f.Exported() {
			v.jsonType(call, f.Pos(), f.Name(), deref(f.Type()), done, true)
		}
	}
	return paths
}

// deepObject is the enc argument of form for a deepObject query parameter.
const deepObject = "deepObject"

// form checks the fields, schema tags, and types of a form body (enc "form"
// or "multipart") or a deepObject query parameter (enc deepObject). seen
// collects the names bound across embedded structs.
func (v *vet) form(call *ast.CallExpr, st *types.Struct, enc string, seen map[string]string, done *typeutil.Map) {
	for i := range st.NumFields() {
		f, tag := st.Field(i), reflect.StructTag(st.Tag(i))
		vf := field(f, tag)
		vf.Kind = formKind(f.Type())
		var walk bool
		var err error
		if enc == deepObject {
			walk, err = geta.CheckDeepObjectField(vf, seen)
		} else {
			walk, err = geta.CheckFormField(vf, enc == "multipart", seen)
		}
		if err != nil {
			v.report(call, f.Pos(), err.Error())
			continue
		}
		if walk {
			v.form(call, f.Type().Underlying().(*types.Struct), enc, seen, done)
			continue
		}
		if _, ok := tag.Lookup("form"); !ok || !f.Exported() {
			continue
		}
		switch vf.Kind {
		case fileKind:
			if s, has := tag.Lookup("schema"); has {
				if err := geta.CheckSchemaTag(s, fileKind); err != nil {
					v.report(call, f.Pos(), fmt.Sprintf("%s: %v", f.Name(), err))
				}
			}
		case "[]" + fileKind:
			v.schemaTag(call, f, tag, true)
		default:
			v.schemaTag(call, f, tag, true)
			v.jsonType(call, f.Pos(), f.Name(), deref(f.Type()), done, true)
		}
	}
}

// fileKind is a geta.File's kind in geta.VetField.
const fileKind = "geta.File"

// isFile reports whether t is geta.File.
func isFile(t types.Type) bool { return qualifiedName(t) == getaPath+".File" }

// formKind is fieldKind, except that a geta.File (after one pointer) is
// "geta.File" and a slice of them "[]geta.File".
func formKind(t types.Type) string {
	e := deref(t)
	if isFile(e) {
		return fileKind
	}
	if s, ok := e.Underlying().(*types.Slice); ok && isFile(s.Elem()) {
		return "[]" + fileKind
	}
	return fieldKind(t)
}

// output checks an output: a JSON body's type, an envelope's fields and
// their types, or a stream's event type.
func (v *vet) output(call *ast.CallExpr, out types.Type) {
	done := new(typeutil.Map)
	if named, ok := out.(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == getaPath {
		if named.Obj().Name() == "Stream" && named.TypeArgs().Len() == 1 {
			v.jsonType(call, call.Pos(), "", named.TypeArgs().At(0), done, false)
		}
		return // *geta.Upgrade has no body
	}
	st, ok := out.Underlying().(*types.Struct)
	if !ok || !isEnvelope(st) {
		v.jsonType(call, call.Pos(), "", out, done, false)
		return
	}
	seen := map[string]bool{}
	var walk func(st *types.Struct)
	walk = func(st *types.Struct) {
		for i := range st.NumFields() {
			f, tag := st.Field(i), reflect.StructTag(st.Tag(i))
			vf := field(f, tag)
			if err := geta.CheckEnvelopeField(vf, seen); err != nil {
				v.report(call, f.Pos(), err.Error())
			}
			if geta.EnvelopeEmbedded(vf) {
				walk(f.Type().Underlying().(*types.Struct))
				continue
			}
			if !f.Exported() {
				continue
			}
			_, header := tag.Lookup("header")
			_, cookie := tag.Lookup("cookie")
			_, body := tag.Lookup("body")
			switch {
			case header:
				v.jsonType(call, f.Pos(), f.Name(), deref(f.Type()), done, false)
			case body && !cookie && !vf.Pointer:
				// A pointer body is refused for being one.
				v.jsonType(call, f.Pos(), f.Name(), f.Type(), done, false)
			}
		}
	}
	walk(st)
}

// status checks a constant success status of a geta.Op or geta.OpNoBody
// call by geta.CheckSuccessStatus and geta.CheckEnvelopeStatus. out is nil
// for OpNoBody. A stream's or an upgrade's status is left to geta.New.
func (v *vet) status(call *ast.CallExpr, out types.Type) {
	// A type-checked call has at least one argument; a multi-value call
	// argument is no constant.
	tv, ok := v.pass.TypesInfo.Types[call.Args[0]]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.Int {
		return
	}
	n, exact := constant.Int64Val(tv.Value)
	if !exact {
		return
	}
	if out != nil {
		if named, isNamed := out.(*types.Named); isNamed && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == getaPath {
			return // a *geta.Stream's or a *geta.Upgrade's
		}
	}
	output, tag, found := "", "", false
	var location *geta.VetField
	if out != nil {
		output = typeString(out)
		tag, found = statusTag(out)
		if st, isStruct := out.Underlying().(*types.Struct); isStruct && isEnvelope(st) {
			location = locationField(st)
		}
	}
	if err := geta.CheckSuccessStatus(int(n), output, tag, location); err != nil {
		v.report(call, call.Pos(), err.Error())
		return
	}
	if !found {
		return
	}
	if err := geta.CheckEnvelopeStatus(int(n), tag); err != nil {
		v.report(call, call.Pos(), err.Error())
	}
}

// locationField returns the envelope's Location header field, searching
// untagged embedded structs, or nil.
func locationField(st *types.Struct) *geta.VetField {
	for i := range st.NumFields() {
		f, tag := st.Field(i), reflect.StructTag(st.Tag(i))
		vf := field(f, tag)
		if h, isH := tag.Lookup("header"); isH && f.Exported() && http.CanonicalHeaderKey(h) == "Location" {
			return &vf
		}
		if geta.EnvelopeEmbedded(vf) {
			if inner, ok := f.Type().Underlying().(*types.Struct); ok {
				if loc := locationField(inner); loc != nil {
					return loc
				}
			}
		}
	}
	return nil
}

// statusTag is the tag of an envelope's status field, in t or a struct it
// embeds untagged.
func statusTag(t types.Type) (string, bool) {
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return "", false
	}
	for i := range st.NumFields() {
		tag := reflect.StructTag(st.Tag(i))
		if s, has := tag.Lookup("status"); has {
			return s, true
		}
		if f := st.Field(i); f.Embedded() && tag == "" {
			if s, has := statusTag(f.Type()); has {
				return s, true
			}
		}
	}
	return "", false
}

// isEnvelope reports whether st or a struct it embeds has a header, cookie,
// body, or status tag, which makes it an envelope rather than a JSON body.
func isEnvelope(st *types.Struct) bool {
	for i := range st.NumFields() {
		tag := reflect.StructTag(st.Tag(i))
		for _, l := range []string{"header", "cookie", "body", "status"} {
			if _, has := tag.Lookup(l); has {
				return true
			}
		}
		f := st.Field(i)
		if inner, ok := f.Type().Underlying().(*types.Struct); ok && f.Embedded() && isEnvelope(inner) {
			return true
		}
	}
	return false
}

// field describes f for geta's field checks.
func field(f *types.Var, tag reflect.StructTag) geta.VetField {
	t := f.Type()
	_, pointer := t.Underlying().(*types.Pointer)
	_, isStruct := t.Underlying().(*types.Struct)
	_, isInterface := t.Underlying().(*types.Interface)
	e := deref(t)
	_, null := nullableOf(e)
	_, object := e.Underlying().(*types.Struct)
	return geta.VetField{Name: f.Name(), Exported: f.Exported(), Embedded: f.Embedded(), Tag: tag,
		Type: typeString(t), Pointer: pointer, Struct: isStruct, Object: object, Interface: isInterface, Cookie: isCookie(t),
		Text:     textMarshaler(e) || textUnmarshaler(e),
		Methods:  ownJSON(e) || writesText(e) || textUnmarshaler(e),
		Kind:     fieldKind(t),
		Nullable: null}
}

// nullableOf returns the value type of t when t is a geta.Nullable.
func nullableOf(t types.Type) (types.Type, bool) {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || qualifiedName(named) != getaPath+".Nullable" || named.TypeArgs().Len() != 1 {
		return nil, false
	}
	return named.TypeArgs().At(0), true
}

// vetType describes t to geta.CheckJSONType.
func vetType(t types.Type) geta.VetType {
	vt := geta.VetType{Type: typeString(t), Kind: kindName(t), Marshaler: writesText(t), Unmarshaler: textUnmarshaler(t),
		Duration: qualifiedName(t) == "time.Duration", Format: namesFormat(t), File: isFile(t)}
	var elem types.Type
	switch u := t.Underlying().(type) {
	case *types.Map:
		// encoding/json/v2 prefers a key's JSON methods to its text methods.
		k := u.Key()
		b, ok := k.Underlying().(*types.Basic)
		vt.StringKey, vt.Key = ok && b.Kind() == types.String, typeString(k)
		if !ownJSON(k) {
			vt.KeyMarshaler, vt.KeyUnmarshaler = writesText(k), textUnmarshaler(k)
		}
		elem = u.Elem()
	case *types.Slice:
		elem = u.Elem()
	}
	if elem != nil {
		_, pointer := elem.Underlying().(*types.Pointer)
		vt.Elem, vt.ElemPointer = typeString(elem), pointer
	}
	return vt
}

// kindName is the kind of t as reflect.Kind writes it.
func kindName(t types.Type) string {
	switch u := t.Underlying().(type) {
	case *types.Basic:
		if u.Kind() == types.UnsafePointer {
			return "unsafe.Pointer"
		}
		return types.Typ[u.Kind()].Name()
	case *types.Pointer:
		return "ptr"
	case *types.Slice:
		return "slice"
	case *types.Array:
		return "array"
	case *types.Map:
		return "map"
	case *types.Chan:
		return "chan"
	case *types.Signature:
		return "func"
	case *types.Struct:
		return "struct"
	}
	return "interface"
}

// typeString writes t as reflect does, so diagnostics match geta.New's:
// "lib.Page[example.com/app/lib.User]", "[]uint8", "interface {}".
func typeString(t types.Type) string {
	var b strings.Builder
	writeType(&b, t, false)
	return b.String()
}

// writeType writes t as reflect does. path qualifies a named type by its
// package path, as in a type argument; package main stays "main".
func writeType(b *strings.Builder, t types.Type, path bool) {
	switch t := types.Unalias(t).(type) {
	case *types.Named:
		if pkg := t.Obj().Pkg(); pkg != nil {
			q := pkg.Name()
			if path && q != "main" {
				q = pkg.Path()
			}
			b.WriteString(q + ".")
		}
		b.WriteString(t.Obj().Name())
		if args := t.TypeArgs(); args.Len() > 0 {
			b.WriteByte('[')
			for i := range args.Len() {
				if i > 0 {
					b.WriteByte(',')
				}
				writeType(b, args.At(i), true)
			}
			b.WriteByte(']')
		}
	case *types.Basic:
		if t.Kind() == types.UnsafePointer {
			b.WriteString("unsafe.Pointer")
			return
		}
		b.WriteString(types.Typ[t.Kind()].Name()) // byte is uint8, rune int32
	case *types.Pointer:
		b.WriteByte('*')
		writeType(b, t.Elem(), path)
	case *types.Slice:
		b.WriteString("[]")
		writeType(b, t.Elem(), path)
	case *types.Array:
		fmt.Fprintf(b, "[%d]", t.Len())
		writeType(b, t.Elem(), path)
	case *types.Map:
		b.WriteString("map[")
		writeType(b, t.Key(), path)
		b.WriteByte(']')
		writeType(b, t.Elem(), path)
	case *types.Chan:
		switch t.Dir() {
		case types.SendOnly:
			b.WriteString("chan<- ")
		case types.RecvOnly:
			b.WriteString("<-chan ")
		default:
			if e, ok := types.Unalias(t.Elem()).(*types.Chan); ok && e.Dir() == types.RecvOnly {
				b.WriteString("chan (")
				writeType(b, t.Elem(), path)
				b.WriteByte(')')
				return
			}
			b.WriteString("chan ")
		}
		writeType(b, t.Elem(), path)
	case *types.Struct:
		if t.NumFields() == 0 {
			b.WriteString("struct {}")
			return
		}
		b.WriteString("struct {")
		for i := range t.NumFields() {
			if i > 0 {
				b.WriteByte(';')
			}
			b.WriteByte(' ')
			f := t.Field(i)
			if !f.Embedded() {
				b.WriteString(f.Name() + " ")
			}
			writeType(b, f.Type(), path)
			if tag := t.Tag(i); tag != "" {
				b.WriteString(" " + strconv.Quote(tag))
			}
		}
		b.WriteString(" }")
	case *types.Interface:
		if t.NumMethods() == 0 {
			b.WriteString("interface {}")
			return
		}
		b.WriteString("interface {")
		for i := range t.NumMethods() {
			if i > 0 {
				b.WriteByte(';')
			}
			m := t.Method(i)
			b.WriteString(" " + m.Name())
			writeSignature(b, m.Type().(*types.Signature), path)
		}
		b.WriteString(" }")
	case *types.Signature:
		b.WriteString("func")
		writeSignature(b, t, path)
	default:
		b.WriteString(t.String()) // a type parameter, judged where it is instantiated
	}
}

// writeSignature writes a function's parameters and results as reflect
// does: "(int, ...string) (bool, error)".
func writeSignature(b *strings.Builder, sig *types.Signature, path bool) {
	b.WriteByte('(')
	for i := range sig.Params().Len() {
		if i > 0 {
			b.WriteString(", ")
		}
		pt := sig.Params().At(i).Type()
		if sig.Variadic() && i == sig.Params().Len()-1 {
			b.WriteString("...")
			pt = pt.(*types.Slice).Elem()
		}
		writeType(b, pt, path)
	}
	b.WriteByte(')')
	switch n := sig.Results().Len(); {
	case n == 1:
		b.WriteByte(' ')
		writeType(b, sig.Results().At(0).Type(), path)
	case n > 1:
		b.WriteString(" (")
		for i := range n {
			if i > 0 {
				b.WriteString(", ")
			}
			writeType(b, sig.Results().At(i).Type(), path)
		}
		b.WriteByte(')')
	}
}

func isCookie(t types.Type) bool {
	p, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := p.Elem().(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "net/http" && named.Obj().Name() == "Cookie"
}

// fieldKind is the geta.VetField kind of a field's type after one pointer,
// or "" for a pointer to a pointer or a slice of pointers.
func fieldKind(t types.Type) string {
	t = deref(t)
	if _, pointer := t.Underlying().(*types.Pointer); pointer {
		return ""
	}
	return valueKind(t)
}

func valueKind(t types.Type) string {
	if _, ok := nullableOf(t); ok {
		return "nullable"
	}
	k := kindOf(t)
	switch {
	case k == "" && ownJSON(t):
		return "json"
	case k == "" && types.IsInterface(t):
		return "union"
	case k == "slice":
		el := t.Underlying().(*types.Slice).Elem()
		if _, pointer := el.Underlying().(*types.Pointer); pointer {
			return ""
		}
		if ek := valueKind(el); ek != "" {
			return "[]" + ek
		}
		return ""
	}
	return k
}

// jsonType checks t as geta.New derives its JSON form: t itself by
// geta.CheckJSONType, then its elements and members recursively. A refusal
// of t is reported at at, prefixed by the field name ("" for none).
//
// A type with its own JSON methods is checked only for carrying a format.
// An interface is not checked: it is sealed by geta.WithUnion, which
// getavet does not read. done holds the types already checked. read reports
// that a request reads t, so schema tags are checked by
// geta.CheckRequestSchemaTag.
func (v *vet) jsonType(call *ast.CallExpr, at token.Pos, name string, t types.Type, done *typeutil.Map, read bool) {
	if _, param := t.(*types.TypeParam); param || types.IsInterface(t) {
		return
	}
	// A Nullable is read as its value, or null.
	if e, ok := nullableOf(t); ok {
		_, nested := nullableOf(e)
		if err := geta.CheckJSONType(geta.VetType{Type: typeString(t), Nullable: true, Elem: typeString(e), ElemNullable: nested}); err != nil {
			v.report(call, at, fieldMessage(name, err))
			return
		}
		v.jsonType(call, at, name, e, done, read)
		return
	}
	if ownJSON(t) {
		if err := geta.CheckJSONType(geta.VetType{Type: typeString(t), JSON: true, Format: namesFormat(t)}); err != nil {
			v.report(call, at, fieldMessage(name, err))
		}
		return
	}
	vt := vetType(t)
	if err := geta.CheckJSONType(vt); err != nil {
		v.report(call, at, fieldMessage(name, err))
		return
	}
	if vt.Marshaler {
		return // a text type, one string
	}
	if checked, ok := done.At(t).(bool); ok {
		// A type met again while its elements and members are being checked
		// holds itself.
		if !checked {
			_, named := types.Unalias(t).(*types.Named)
			_, isStruct := t.Underlying().(*types.Struct)
			if err := geta.CheckSelfHolding(typeString(t), named && isStruct); err != nil {
				v.report(call, at, fieldMessage(name, fmt.Errorf("%s%w", strings.Join(v.elems, ""), err)))
			}
		}
		return // checked already, or being checked
	}
	done.Set(t, false)
	defer done.Set(t, true)
	// geta.New prefixes an element's refusal with the slice or map.
	elem := func(e types.Type) {
		v.elems = append(v.elems, typeString(t)+": ")
		v.jsonType(call, at, name, e, done, read)
		v.elems = v.elems[:len(v.elems)-1]
	}
	switch u := t.Underlying().(type) {
	case *types.Slice:
		if b, ok := u.Elem().Underlying().(*types.Basic); !ok || b.Kind() != types.Uint8 {
			elem(u.Elem())
		}
	case *types.Map:
		// The key before the value, in geta.New's order.
		v.jsonType(call, at, name, u.Key(), done, read)
		elem(u.Elem())
	case *types.Struct:
		// A named struct is a component; its name is checked first.
		if _, named := types.Unalias(t).(*types.Named); named {
			if err := geta.CheckSchemaName(typeString(t)); err != nil {
				v.report(call, at, fieldMessage(name, err))
				return
			}
		}
		v.members(call, at, name, t, u, done, read)
	}
}

// fieldMessage prefixes err with the field name, as geta.New does.
func fieldMessage(name string, err error) string {
	if name == "" {
		return err.Error()
	}
	return name + ": " + err.Error()
}

// members checks a struct's fields, embedded ones included, as JSON
// members. read is as for jsonType.
func (v *vet) members(call *ast.CallExpr, at token.Pos, name string, t types.Type, st *types.Struct, done *typeutil.Map, read bool) {
	names := map[string]bool{}
	refused := false
	var walk func(st *types.Struct)
	walk = func(st *types.Struct) {
		for i := range st.NumFields() {
			f, tag := st.Field(i), reflect.StructTag(st.Tag(i))
			vf := field(f, tag)
			member, embedded, err := geta.CheckMemberField(vf, names)
			if err != nil {
				v.report(call, f.Pos(), err.Error())
				refused = true
			}
			// An embedded struct refused for its own tag still has members
			// to check.
			jsonName, _, _ := strings.Cut(tag.Get("json"), ",")
			if embedded || (err != nil && vf.Embedded && vf.Struct && !vf.Methods && jsonName == "" && tag.Get("json") != "-") {
				walk(f.Type().Underlying().(*types.Struct))
				continue
			}
			if member == "" || err != nil {
				continue
			}
			v.schemaTag(call, f, tag, read)
			// A field's diagnostics are named from the field.
			outer := v.elems
			v.elems = nil
			v.jsonType(call, f.Pos(), f.Name(), deref(f.Type()), done, read)
			v.elems = outer
		}
	}
	walk(st)
	// geta.New stops at a refused field before counting members.
	if refused {
		return
	}
	if err := geta.CheckStructMembers(typeString(t), st.NumFields(), len(names)); err != nil {
		v.report(call, at, fieldMessage(name, err))
	}
}

// schemaTag checks f's schema tag by geta.CheckRequestSchemaTag if read,
// and by geta.CheckSchemaTag otherwise.
func (v *vet) schemaTag(call *ast.CallExpr, f *types.Var, tag reflect.StructTag, read bool) {
	s, has := tag.Lookup("schema")
	if !has {
		return
	}
	kind := kindOf(f.Type())
	if kind == "" {
		return // geta.New names a type with no JSON form
	}
	if _, named := types.Unalias(deref(f.Type())).(*types.Named); kind == "struct" && !named {
		kind = "object" // an unnamed struct's schema is an object written in place
	}
	switch kind {
	case "slice":
		kind = sliceKind(deref(f.Type()))
	case "map":
		kind = mapKind(deref(f.Type()))
	}
	check := geta.CheckSchemaTag
	if read {
		check = geta.CheckRequestSchemaTag
	}
	if err := check(s, kind); err != nil {
		v.report(call, f.Pos(), fmt.Sprintf("%s: %v", f.Name(), err))
	}
}

// sliceKind is the geta.CheckSchemaTag kind of slice t: "[]" and the
// element's kind, so items. keywords are checked against the element. It is
// "slice" when the element has no kind (a pointer, or a type with its own
// JSON methods).
func sliceKind(t types.Type) string {
	el := t.Underlying().(*types.Slice).Elem()
	if _, pointer := el.Underlying().(*types.Pointer); pointer {
		return "slice"
	}
	if isFile(el) {
		return "[]" + fileKind
	}
	k := elemKind(el)
	if k == "" {
		return "slice"
	}
	return "[]" + k
}

// mapKind is the geta.CheckSchemaTag kind of map t: "map[key]value", so
// propertyNames. and additionalProperties. keywords are checked against the
// key and value. The value part is empty when the value has no kind, and
// the result is "map" when neither part has one.
func mapKind(t types.Type) string {
	m := t.Underlying().(*types.Map)
	key := keyKind(m.Key())
	k := ""
	if _, pointer := m.Elem().Underlying().(*types.Pointer); !pointer {
		k = elemKind(m.Elem())
	}
	if k == "" && key == "string" {
		return "map"
	}
	return "map[" + key + "]" + k
}

// keyKind is the geta.CheckSchemaTag kind of a map key: the kind of a key
// type that carries a format ("format", "geta.Password"), or "string".
func keyKind(k types.Type) string {
	if kind := kindOf(k); kind == "format" || strings.HasPrefix(kind, "geta.") {
		return kind
	}
	return "string"
}

// elemKind is the geta.CheckSchemaTag kind of a slice element or map value,
// or "".
func elemKind(el types.Type) string {
	k := kindOf(el)
	switch k {
	case "slice":
		k = sliceKind(el)
	case "map":
		k = mapKind(el)
	case "struct":
		if _, named := types.Unalias(el).(*types.Named); !named {
			k = "object"
		}
	}
	return k
}

func deref(t types.Type) types.Type {
	if p, ok := t.Underlying().(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

// kindOf is the geta.CheckSchemaTag kind of t, or "" for a type geta
// derives no schema for.
func kindOf(t types.Type) string {
	t = deref(t)
	// A Nullable's kind is its value's, after "?".
	if e, ok := nullableOf(t); ok {
		if k := kindOf(e); k != "" && !strings.HasPrefix(k, "?") {
			return "?" + k
		}
		return ""
	}
	// A format type is its own kind, whose schema has its format.
	switch q := qualifiedName(t); {
	case q == "time.Time", q == "uuid.UUID":
		return q
	case isFile(t):
		return "" // geta.New names a geta.File outside a multipart body (form)
	case strings.HasPrefix(q, getaPath+"."):
		if kind := "geta." + strings.TrimPrefix(q, getaPath+"."); geta.CheckSchemaTag("", kind) == nil {
			return kind
		}
	}
	if ownJSON(t) {
		// Its schema comes from geta.WithSchema, which getavet does not read.
		return ""
	}
	if isText(t) {
		// A geta.FormatType carries its own format.
		if namesFormat(t) {
			return "format"
		}
		return "text"
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch u.Kind() {
		case types.String, types.Bool, types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
			types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Float32, types.Float64:
			return types.Typ[u.Kind()].Name()
		}
	case *types.Slice:
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Uint8 {
			return "[]byte"
		}
		return "slice"
	case *types.Map:
		return "map"
	case *types.Struct:
		return "struct"
	}
	return ""
}

// qualifiedName is a named type's package path and name ("time.Duration"),
// or "" for another type.
func qualifiedName(t types.Type) string {
	if named, ok := types.Unalias(t).(*types.Named); ok && named.Obj().Pkg() != nil {
		return named.Obj().Pkg().Path() + "." + named.Obj().Name()
	}
	return ""
}

// isText reports whether t is written as a JSON string through a text
// method.
func isText(t types.Type) bool { return writesText(t) }

var (
	bytesType = types.NewSlice(types.Typ[types.Byte])
	errorType = types.Universe.Lookup("error").Type()

	// marshalText and unmarshalText also match MarshalJSON and
	// UnmarshalJSON.
	marshalText   = signature(nil, []types.Type{bytesType, errorType})
	unmarshalText = signature([]types.Type{bytesType}, []types.Type{errorType})
	appendText    = signature([]types.Type{bytesType}, []types.Type{bytesType, errorType})
)

func signature(params, results []types.Type) *types.Signature {
	tuple := func(ts []types.Type) *types.Tuple {
		vs := make([]*types.Var, len(ts))
		for i, t := range ts {
			vs[i] = types.NewParam(token.NoPos, nil, "", t)
		}
		return types.NewTuple(vs...)
	}
	return types.NewSignatureType(nil, nil, nil, tuple(params), tuple(results), false)
}

// method reports whether t's method set has name with signature want.
func method(t types.Type, name string, want *types.Signature) bool {
	sel := types.NewMethodSet(t).Lookup(nil, name)
	return sel != nil && types.Identical(sel.Type(), want)
}

// either reports whether t or *t has the method name with the signature
// want.
func either(t types.Type, name string, want *types.Signature) bool {
	return method(t, name, want) || method(types.NewPointer(t), name, want)
}

// textMarshaler reports whether t or *t implements encoding.TextMarshaler.
func textMarshaler(t types.Type) bool { return either(t, "MarshalText", marshalText) }

// writesText reports whether t or *t implements encoding.TextAppender or
// encoding.TextMarshaler.
func writesText(t types.Type) bool { return textMarshaler(t) || either(t, "AppendText", appendText) }

// textUnmarshaler reports whether *t implements encoding.TextUnmarshaler.
func textUnmarshaler(t types.Type) bool {
	return method(types.NewPointer(t), "UnmarshalText", unmarshalText)
}

// schemaFormat is the signature of geta.FormatType's method.
var schemaFormat = signature(nil, []types.Type{types.Typ[types.String]})

// namesFormat reports whether t or *t has SchemaFormat() string.
func namesFormat(t types.Type) bool { return either(t, "SchemaFormat", schemaFormat) }

// ownJSON reports whether t or *t has an encoding/json/v2 JSON method.
// Callers recognise time.Time first.
func ownJSON(t types.Type) bool {
	if either(t, "MarshalJSON", marshalText) || either(t, "UnmarshalJSON", unmarshalText) {
		return true
	}
	for _, tt := range []types.Type{t, types.NewPointer(t)} {
		if streamMethod(tt, "MarshalJSONTo", "Encoder") || streamMethod(tt, "UnmarshalJSONFrom", "Decoder") {
			return true
		}
	}
	return false
}

// streamMethod reports whether t's method set has the method name taking a
// *jsontext.Encoder or *jsontext.Decoder (arg) and returning an error.
func streamMethod(t types.Type, name, arg string) bool {
	sel := types.NewMethodSet(t).Lookup(nil, name)
	if sel == nil {
		return false
	}
	sig, ok := sel.Type().(*types.Signature)
	return ok && !sig.Variadic() && sig.Params().Len() == 1 && sig.Results().Len() == 1 &&
		types.TypeString(sig.Params().At(0).Type(), nil) == "*encoding/json/jsontext."+arg &&
		types.Identical(sig.Results().At(0).Type(), errorType)
}

// routeURL is the URL of the package's directory when it lies below a
// directory holding the table geta sync writes.
func routeURL(pass *analysis.Pass) (string, bool) {
	// go/packages drivers pass a directory holding only an x_test package
	// with no files.
	if len(pass.Files) == 0 {
		return "", false
	}
	dir := filepath.Dir(pass.Fset.File(pass.Files[0].Pos()).Name())
	for root := dir; ; root = filepath.Dir(root) {
		if _, err := os.Stat(filepath.Join(root, tree.TableFile)); err == nil {
			// root is an ancestor of dir, so Rel cannot fail.
			rel, _ := filepath.Rel(root, dir)
			rel = filepath.ToSlash(rel)
			if rel == "." {
				rel = ""
			}
			url, err := tree.URLFor(rel)
			return url, err == nil
		}
		if filepath.Dir(root) == root {
			return "", false
		}
	}
}

// urlParams lists a template's parameter names.
func urlParams(url string) []string {
	var out []string
	for seg := range strings.SplitSeq(url, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, seg[1:len(seg)-1])
		}
	}
	return out
}
