package natsrouter

// registrar is the route-registration surface shared by *Router and *Group.
// The Register/RegisterNoBody/RegisterVoid helpers target it so routes can be
// registered on either the base router or a middleware group.
type registrar interface {
	addRoute(pattern string, handlers []HandlerFunc)
}

var (
	_ registrar = (*Router)(nil)
	_ registrar = (*Group)(nil)
)

// Group carries additional middleware that runs after the parent's middleware
// for every route registered on it. Create one with (*Router).Group. Routes on
// a group inherit the router's global middleware (prepended by Router.addRoute)
// followed by the group's middleware, then the route handler.
type Group struct {
	parent     registrar
	middleware []HandlerFunc
}

// Group returns a route group whose registered routes run mw after the router's
// global middleware (installed via Use) and before the route handler.
func (r *Router) Group(mw ...HandlerFunc) *Group {
	return &Group{parent: r, middleware: mw}
}

func (g *Group) addRoute(pattern string, handlers []HandlerFunc) {
	all := make([]HandlerFunc, 0, len(g.middleware)+len(handlers))
	all = append(all, g.middleware...)
	all = append(all, handlers...)
	g.parent.addRoute(pattern, all)
}
