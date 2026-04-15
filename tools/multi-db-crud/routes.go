package main

import "github.com/gin-gonic/gin"

func registerRoutes(r *gin.Engine, h *handler) {
	r.GET("/healthz", h.healthz)

	api := r.Group("/api")

	// Connection management
	api.POST("/connect", h.connect)
	api.GET("/connections", h.listConnections)
	api.DELETE("/connections/:id", h.deleteConnection)

	// MongoDB routes
	mongo := api.Group("/mongo/:id")
	mongo.GET("/collections", h.mongoCollections)
	mongo.GET("/collections/:name/docs", h.mongoDocs)
	mongo.POST("/collections/:name/docs", h.mongoCreateDoc)
	mongo.PUT("/collections/:name/docs/:docID", h.mongoUpdateDoc)
	mongo.DELETE("/collections/:name/docs/:docID", h.mongoDeleteDoc)
	mongo.GET("/collections/:name/export", h.mongoExport)
	mongo.POST("/collections/:name/import", h.mongoImport)

	// Cassandra routes
	cass := api.Group("/cassandra/:id")
	cass.GET("/tables", h.cassandraTables)
	cass.GET("/tables/:name/rows", h.cassandraRows)
	cass.POST("/tables/:name/rows", h.cassandraCreateRow)
	cass.PUT("/tables/:name/rows", h.cassandraUpdateRow)
	cass.DELETE("/tables/:name/rows", h.cassandraDeleteRow)
	cass.GET("/tables/:name/export", h.cassandraExport)
	cass.POST("/tables/:name/import", h.cassandraImport)

	// Template routes
	api.GET("/templates", h.listTemplates)
	api.GET("/templates/:kind", h.getTemplate)

	// Serve static UI for all other paths
	r.NoRoute(h.serveUI)
}
