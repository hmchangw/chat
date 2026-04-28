package natsrouter

// runChain executes the handler chain against c; for tests in this package only.
func runChain(c *Context, handlers []HandlerFunc) {
	cs := chainPool.Get().(*chainState)
	cs.handlers = handlers
	cs.index = -1
	c.chain = cs
	c.Next()
	releaseContext(c)
}
