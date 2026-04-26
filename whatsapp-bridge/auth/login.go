package auth

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"whatsapp-bridge/config"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	Service string `json:"service"`
	jwt.RegisteredClaims
}

func LoginHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := []byte("Bearer " + cfg.APIKey)
		got := []byte(r.Header.Get("Authorization"))

		if subtle.ConstantTimeCompare(expected, got) != 1 {
			slog.Warn("login rejected: bad api key", "remote", r.RemoteAddr)
			http.Error(w, "Invalid credentials", http.StatusUnauthorized)
			return
		}

		claims := Claims{
			Service: "mcp-server",
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(45 * time.Minute)),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
			},
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(cfg.JWTSecret)
		if err != nil {
			slog.Error("failed to sign token", "err", err)
			http.Error(w, "Failed to generate token", http.StatusInternalServerError)
			return
		}

		json.NewEncoder(w).Encode(map[string]string{"token": signed})
	}
}

// JwtAuthMiddleware Protect normal API endpoints
func JwtAuthMiddleware(cfg *config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "Missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(auth, "Bearer ")

		token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
			return cfg.JWTSecret, nil
		})

		if err != nil {
			slog.Warn("jwt parse error", "err", err, "remote", r.RemoteAddr)
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		if !token.Valid {
			slog.Warn("jwt invalid", "remote", r.RemoteAddr)
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
