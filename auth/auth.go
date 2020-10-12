package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.sgu.ru/ultramarine/logserver/conf"

	"github.com/dgrijalva/jwt-go/v4"
	"github.com/go-ldap/ldap/v3"
	"github.com/gorilla/securecookie"
	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/auth"
	grpc_ctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type claims struct {
	jwt.StandardClaims
	Username string `json:"username"`
}

var (
	hashKey    []byte
	blockKey   []byte
	infoCookie *securecookie.SecureCookie
	sigCookie  *securecookie.SecureCookie
	tls        bool
)

// InitKeysAndCookies initializes keys and cookies.
func InitKeysAndCookies(TLS bool) {
	hashKey = []byte(conf.Config.HashKey)
	blockKey = []byte(conf.Config.BlockKey)

	infoCookie = securecookie.New(hashKey, blockKey)
	sigCookie = securecookie.New(hashKey, blockKey)

	tls = TLS
}

func createToken(username, password string) (string, error) {
	if err := authenticate(username, password); err != nil {
		return "", err
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, &claims{
		StandardClaims: jwt.StandardClaims{
			ExpiresAt: jwt.At(time.Now().Add(time.Hour * 24 * 30)),
			IssuedAt:  jwt.At(time.Now()),
		},
		Username: username,
	})

	return token.SignedString([]byte(conf.Config.JWTKey))
}

func parseToken(tokenString string) (string, error) {
	token, err := jwt.ParseWithClaims(tokenString, &claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
		}

		return []byte(conf.Config.JWTKey), nil
	})

	if customClaims, ok := token.Claims.(*claims); ok && token.Valid {
		return customClaims.Username, nil
	}

	return "", err
}

// LDAPAuthFunc is used by a middleware to authenticate requests.
func LDAPAuthFunc(ctx context.Context) (context.Context, error) {
	token, err := grpc_auth.AuthFromMD(ctx, "bearer")
	if err != nil {
		log.Infof("Failed to get token from metadata: %s", err)
		return nil, err
	}

	username, err := parseToken(token)
	if err != nil {
		log.Infof("Failed to parse token: %s", err)
		return nil, status.Errorf(codes.Unauthenticated, "invalid auth token: %v", err)
	}

	grpc_ctxtags.Extract(ctx).Set("auth.sub", username)

	type ctxKey string
	k := ctxKey("tokenInfo")
	newCtx := context.WithValue(ctx, k, username)

	return newCtx, nil
}

// Handler handles auth endpoint.
func Handler(w http.ResponseWriter, r *http.Request) {
	var loginInfo struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&loginInfo); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		log.Infof("Failed to decode auth request: %s", err)
		return
	}

	token, err := createToken(loginInfo.Username, loginInfo.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		log.Infof("Failed to create token for %s: %s", loginInfo.Username, err)
		return
	}

	tokenParts := strings.Split(token, ".")

	infoEncoded, err := infoCookie.Encode("info", fmt.Sprintf("%s.%s", tokenParts[0], tokenParts[1]))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Warnf("Failed to encode info part of token: %s", err)
		return
	}
	sigEncoded, err := sigCookie.Encode("sig", tokenParts[2])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Warnf("Failed to encode sig part of token: %s", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "info",
		Value:    infoEncoded,
		Secure:   tls,
		SameSite: 3,
		MaxAge:   86400 * 30,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "sig",
		Value:    sigEncoded,
		Secure:   tls,
		SameSite: 3,
		HttpOnly: true,
	})
}

// LogoutHandler handles logout button.
func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "info",
		Value:    "",
		Secure:   tls,
		SameSite: 3,
		MaxAge:   86400 * 30,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "sig",
		Value:    "",
		Secure:   tls,
		SameSite: 3,
		HttpOnly: true,
	})
}

// TwoCookieAuthMiddleware used for SPA auth.
func TwoCookieAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r)
		} else {
			infoPart, err := r.Cookie("info")
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			sigPart, err := r.Cookie("sig")
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			var (
				infoStr string
				sigStr  string
			)

			if err = infoCookie.Decode("info", infoPart.Value, &infoStr); err != nil {
				next.ServeHTTP(w, r)
				return
			}
			if err = sigCookie.Decode("sig", sigPart.Value, &sigStr); err != nil {
				next.ServeHTTP(w, r)
				return
			}

			r.Header.Set("Authorization", fmt.Sprintf("Bearer %s.%s", infoStr, sigStr))

			next.ServeHTTP(w, r)
		}
	})
}

func authenticate(login, password string) error {
	if password == "" {
		return errors.New("empty password")
	}

	l, err := ldap.Dial("tcp", conf.Config.LDAPHost)
	if err != nil {
		return err
	}
	defer l.Close()

	if err = l.Bind(conf.Config.LDAPBindDN, conf.Config.LDAPBindPass); err != nil {
		return fmt.Errorf("error authenticating admin user in LDAP: %s", err)
	}

	searchRequest := ldap.NewSearchRequest(
		conf.Config.LDAPBaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		"(&(sAMAccountName="+login+"))",
		[]string{"cn"},
		nil,
	)

	sr, err := l.Search(searchRequest)
	if err != nil {
		return fmt.Errorf("error searching user in LDAP: %s", err)
	}
	if len(sr.Entries) != 1 {
		return errors.New("user not found in LDAP")
	}

	username := sr.Entries[0].GetAttributeValue("cn")

	if err = l.Bind(username, password); err != nil {
		return fmt.Errorf("error authenticating user in LDAP: %s", err)
	}

	return nil
}
