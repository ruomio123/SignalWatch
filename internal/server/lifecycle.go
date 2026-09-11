package server

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"signalwatch/internal/platform/httpx"
	"time"
)

// Serve owns HTTP admission and shutdown; resources are closed by the caller
// only after in-flight handlers have drained or the shutdown deadline expires.
func Serve(ctx context.Context, address string, handler http.Handler) error {
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			<-done
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func requestBudget() gin.HandlerFunc {
	return func(c *gin.Context) {
		const limit = 64 << 10
		if c.Request.ContentLength > limit {
			httpx.WriteError(c, http.StatusRequestEntityTooLarge, httpx.CodeValidationError, "request body too large")
			c.Abort()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 35*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
