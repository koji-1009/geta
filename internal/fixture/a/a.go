// Package a and package b each declare a User, for the schema-name
// collision test.
package a

type User struct {
	Name string `json:"name"`
}
