package auth

import "github.com/golang-jwt/jwt/v5"

// Claims contains only stable authentication data. User profile fields do not
// belong in an access token because they can change before the token expires.
type Claims struct {
	jwt.RegisteredClaims
}
