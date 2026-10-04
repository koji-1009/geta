// Package model is the register example's domain.
package model

import "time"

// Role is a user's role. Its vocabulary is written in the schema tag of
// every field that carries one: a struct tag cannot name a constant.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// User is the wire form and the stored form of a user.
type User struct {
	ID     string   `json:"id" schema:"minLength=1,maxLength=64,pattern=^[A-Za-z0-9_-]+$"`
	Name   string   `json:"name" schema:"minLength=1,maxLength=100"`
	Age    *int     `json:"age,omitzero" schema:"minimum=0,maximum=150"`
	Role   Role     `json:"role" schema:"enum=admin|member"`
	Tags   []string `json:"tags" schema:"maxItems=16,uniqueItems=true"`
	Active *bool    `json:"active,omitzero"`
	// Balance is an exact decimal, as its digits.
	Balance   *string    `json:"balance,omitzero" schema:"maxLength=32,pattern=^-?[0-9]+(\\.[0-9]+)?$"`
	CreatedAt *time.Time `json:"createdAt,omitzero"`
}

// UserList is one page of users and the size of the whole match.
type UserList struct {
	Items []User `json:"items"`
	Total int    `json:"total"`
}
