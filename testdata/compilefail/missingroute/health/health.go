// A route directory whose package does not export Route.
package health

import "github.com/koji-1009/geta"

func Routes(env *int) geta.Route { return geta.Route{} }
