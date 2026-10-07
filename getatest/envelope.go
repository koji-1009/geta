package getatest

import (
	"bytes"
	"encoding"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"

	"github.com/koji-1009/geta/internal/vet"
)

var (
	readerType      = reflect.TypeFor[io.Reader]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// readEnvelope reads an envelope's status, headers, cookies, and body into
// out, descending into embedded structs (vet.EnvelopeEmbedded).
func readEnvelope(res *http.Response, body []byte, out reflect.Value, opts json.Options) error {
	t := out.Type()
	for i := range t.NumField() {
		f, fv := t.Field(i), out.Field(i)
		if vet.EnvelopeEmbedded(vet.FieldOf(f)) {
			if err := readEnvelope(res, body, fv, opts); err != nil {
				return err
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if _, ok := f.Tag.Lookup("status"); ok {
			fv.SetInt(int64(res.StatusCode))
			continue
		}
		if name, ok := f.Tag.Lookup("header"); ok {
			if vs := res.Header.Values(name); len(vs) > 0 {
				if err := setText(fv, vs[0]); err != nil {
					return fmt.Errorf("header %s: %w", name, err)
				}
			}
		}
		if name, ok := f.Tag.Lookup("cookie"); ok {
			for _, ck := range res.Cookies() {
				if ck.Name == name {
					fv.Set(reflect.ValueOf(ck))
				}
			}
		}
		if mt, ok := f.Tag.Lookup("body"); ok && mt != "json" {
			// A media-type body: the raw bytes.
			if f.Type == readerType {
				fv.Set(reflect.ValueOf(bytes.NewReader(body)))
			} else {
				fv.SetBytes(body)
			}
			continue
		}
		if _, ok := f.Tag.Lookup("body"); ok && len(body) > 0 {
			if err := json.Unmarshal(body, fv.Addr().Interface(), opts); err != nil {
				return fmt.Errorf("body: %w", err)
			}
		}
	}
	return nil
}

// isEnvelope reports whether t or a struct it embeds has a header, cookie,
// body, or status tag, which makes it an envelope rather than a JSON body.
func isEnvelope(t reflect.Type) bool {
	for i := range t.NumField() {
		f := t.Field(i)
		for _, l := range []string{"header", "cookie", "body", "status"} {
			if _, ok := f.Tag.Lookup(l); ok {
				return true
			}
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && isEnvelope(f.Type) {
			return true
		}
	}
	return false
}

// setText stores a header's text in v, allocating a pointer field.
func setText(v reflect.Value, s string) error {
	if v.Kind() == reflect.Pointer {
		v.Set(reflect.New(v.Type().Elem()))
		v = v.Elem()
	}
	if reflect.PointerTo(v.Type()).Implements(textUnmarshaler) {
		return v.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s))
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
		return nil
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		v.SetBool(b)
		return err
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, v.Type().Bits())
		v.SetInt(n)
		return err
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, v.Type().Bits())
		v.SetUint(n)
		return err
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, v.Type().Bits())
		v.SetFloat(f)
		return err
	}
	return errors.New("type " + v.Type().String() + " is not a header type")
}
