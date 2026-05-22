// Package middleware holds project-specific HTTP middleware. Imported under
// the alias apimw to avoid a name clash with go-chi/middleware.
package middleware

import (
	"net/http"

	"github.com/google/uuid"

	"gophprofile/internal/logging"
)

// HeaderRequestID is the canonical HTTP header for request correlation.
const HeaderRequestID = "X-Request-Id"

// RequestID stamps an inbound request with a correlation id: it reuses the
// X-Request-Id header if the client supplied one, otherwise it generates a
// fresh UUID v4. The id is stored on ctx via logging.WithRequestID so that any
// slog.*Context call downstream picks it up automatically, and echoed back in
// the response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}
