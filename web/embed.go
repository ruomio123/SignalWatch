// Package web embeds and serves the SignalWatch browser application.
package web

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

// staticFiles is embedded into the API binary so local and production
// deployments do not need a separate frontend process.
//
//go:embed static
var staticFiles embed.FS

var appFiles = mustSub(staticFiles, "static")
var indexHTML = mustReadFile(appFiles, "index.html")

// RegisterRoutes adds the browser application without changing the JSON API's
// not-found behaviour. Only known client-side routes serve the application
// shell; unknown paths still reach Gin's JSON NoRoute handler.
func RegisterRoutes(router *gin.Engine) {
	index := func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Header("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	}

	for _, route := range []string{"/", "/login", "/register", "/app", "/subscriptions", "/settings"} {
		router.GET(route, index)
	}

	assets := http.FileServer(http.FS(appFiles))
	router.GET("/assets/*filepath", func(c *gin.Context) {
		// Assets do not have content-hashed file names. Revalidate them so a
		// restarted embedded binary cannot leave browsers on stale UI code.
		c.Header("Cache-Control", "no-cache")
		assets.ServeHTTP(c.Writer, c.Request)
	})
}

func mustSub(source fs.FS, directory string) fs.FS {
	sub, err := fs.Sub(source, directory)
	if err != nil {
		panic(err)
	}
	return sub
}

func mustReadFile(source fs.FS, name string) []byte {
	contents, err := fs.ReadFile(source, name)
	if err != nil {
		panic(err)
	}
	return contents
}
