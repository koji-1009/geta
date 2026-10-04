package geta_test

import (
	"encoding/json/v2"
	"fmt"

	"github.com/koji-1009/geta"
)

// ProfilePatch is a JSON Merge Patch (RFC 7396) of a profile: each member
// tagged omitzero may be left out (keep the stored value), and a
// geta.Nullable member may also be null (clear it).
type ProfilePatch struct {
	Name    *string                `json:"name,omitzero" schema:"minLength=1"`
	Website geta.Nullable[string]  `json:"website,omitzero" schema:"maxLength=200"`
	Age     geta.Nullable[float64] `json:"age,omitzero" schema:"minimum=0"`
}

// A Nullable tells a member left out from null and from a value, the three
// states a merge patch needs; a pointer would read null and absent alike.
// geta binds a request body this way; encoding/json/v2 does the same.
func ExampleNullable() {
	var p ProfilePatch
	if err := json.Unmarshal([]byte(`{"website":null,"age":36}`), &p); err != nil {
		panic(err)
	}
	if v, ok := p.Website.Get(); ok {
		fmt.Println("website set to", v)
	} else if p.Website.IsNull() {
		fmt.Println("website cleared")
	} else {
		fmt.Println("website kept")
	}
	if v, ok := p.Age.Get(); ok {
		fmt.Println("age set to", v)
	}
	fmt.Println("name kept:", p.Name == nil)

	out, _ := json.Marshal(ProfilePatch{Website: geta.Null[string](), Age: geta.NotNull(37.0)})
	fmt.Println(string(out))
	// Output:
	// website cleared
	// age set to 36
	// name kept: true
	// {"website":null,"age":37}
}
