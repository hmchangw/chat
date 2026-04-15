package natsrouter

// RouterGroup groups routes that share middleware. It implements
// Registrar so typed handlers (Register, RegisterNoBody, RegisterVoid)
// can be registered on a group exactly like on a Router.
//
// Create a group via Router.Group or RouterGroup.Group.
// Middleware added to a group only applies to routes registered on that group.
type RouterGroup struct {
	parent   Registrar
	handlers []HandlerFunc
}

// Group creates a new RouterGroup with the given middleware.
// The group delegates route registration to the Router (via parent chain).
// Middleware chain order: router middleware → parent group(s) → this group → handler.
func (r *Router) Group(mw ...HandlerFunc) *RouterGroup {
	return &RouterGroup{
		parent:   r,
		handlers: copyHandlers(mw),
	}
}

// Group creates a nested sub-group that inherits the parent group's middleware.
func (g *RouterGroup) Group(mw ...HandlerFunc) *RouterGroup {
	return &RouterGroup{
		parent:   g,
		handlers: copyHandlers(mw),
	}
}

// Use appends middleware to this group's chain.
func (g *RouterGroup) Use(mw ...HandlerFunc) {
	g.handlers = append(g.handlers, mw...)
}

// addRoute prepends the group's middleware to the handler slice,
// then delegates to the parent Registrar. When the parent is a Router,
// the router's own middleware is prepended there — producing the full chain:
// router middleware → group middleware → handler.
func (g *RouterGroup) addRoute(pattern string, handlers []HandlerFunc) {
	all := make([]HandlerFunc, 0, len(g.handlers)+len(handlers))
	all = append(all, g.handlers...)
	all = append(all, handlers...)
	g.parent.addRoute(pattern, all)
}

// copyHandlers returns a copy of the slice to prevent mutation of the caller's slice.
func copyHandlers(mw []HandlerFunc) []HandlerFunc {
	if len(mw) == 0 {
		return nil
	}
	cp := make([]HandlerFunc, len(mw))
	copy(cp, mw)
	return cp
}
