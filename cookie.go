package geta

import (
	"fmt"
	"net/http"
	"reflect"
)

// cookieType is the type of an envelope field tagged cookie:"name". A nil
// field sends nothing; otherwise the cookie is checked by [http.Cookie.Valid]
// and written by [http.Cookie.String]. The tag names the cookie. A Name set
// to anything else, or a cookie Valid refuses, is a defect and answers 500.
var cookieType = reflect.TypeFor[*http.Cookie]()

// validCookieName reports whether name is a cookie name net/http accepts.
func validCookieName(name string) bool {
	return (&http.Cookie{Name: name}).Valid() == nil
}

// setCookie is the Set-Cookie header value for c under the tag's name.
func setCookie(name string, c *http.Cookie) (string, error) {
	if c.Name != "" && c.Name != name {
		return "", fmt.Errorf("the cookie's Name is %q, but its field is tagged %q", c.Name, name)
	}
	named := *c
	named.Name = name
	if err := named.Valid(); err != nil {
		return "", err
	}
	return named.String(), nil
}
