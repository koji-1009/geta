// A route whose Route takes something other than the tree's Env.
package health

import "github.com/koji-1009/geta"

type Other struct{}

func Route(env *Other) geta.Route { return geta.Route{} }
