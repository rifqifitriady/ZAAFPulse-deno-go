package provauth

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strconv"
	"time"
)

const (
	googleTokenURL = "https://oauth2.googleapis.com/token"
	jwtBearer      = "urn:ietf:params:oauth:grant-type:jwt-bearer"
)

// GoogleServiceAccount exchanges a JWT signed with the service account's key (RS256) for a token, at the account's
// token_uri (default https://oauth2.googleapis.com/token). The stored secret is the service account's JSON.
func GoogleServiceAccount(saJSON Secret, scope string, hc *http.Client, clock Clock, opts ...Option) *Cached {
	return newCached(func(_ string, now time.Time) (string, []field, error) {
		raw, err := saJSON()
		if err != nil {
			return "", nil, err
		}
		if raw == "" {
			return "", nil, formError("no credential stored for google-sa")
		}
		aud, jwt, err := signJWT(raw, scope, now)
		if err != nil {
			return "", nil, err
		}
		return aud, []field{{"grant_type", jwtBearer}, {"assertion", jwt}}, nil
	}, hc, clock, opts)
}

// signJWT builds the assertion as providers.js does: the header {"alg":"RS256","typ":"JWT"} and the claims
// {iss, scope, aud, iat, exp} written as JSON.stringify writes them (that key order; iss left out when the account has
// no client_email, null when it is null), base64url without padding, signed with RSASSA-PKCS1-v1_5 SHA-256, which is
// deterministic: the fixtures compare the whole assertion. iat is the clock's Unix seconds (floor), exp iat + 3600.
// It returns the audience, which is also the token endpoint.
func signJWT(raw, scope string, now time.Time) (aud, jwt string, err error) {
	var sa map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &sa) != nil || sa == nil {
		return "", "", formError("the credential is not a service account JSON")
	}
	iss, hasIss, isNull, err := saString(sa, "client_email")
	if err != nil {
		return "", "", err
	}
	aud, _, _, err = saString(sa, "token_uri")
	if err != nil {
		return "", "", err
	}
	if aud == "" {
		aud = googleTokenURL
	}
	key, err := saKey(sa["private_key"])
	if err != nil {
		return "", "", err
	}
	t := now.Unix()
	claims := "{"
	switch {
	case isNull:
		claims += `"iss":null,`
	case hasIss:
		claims += `"iss":` + jsString(iss) + ","
	}
	claims += `"scope":` + jsString(scope) + `,"aud":` + jsString(aud) +
		`,"iat":` + strconv.FormatInt(t, 10) + `,"exp":` + strconv.FormatInt(t+3600, 10) + "}"
	b64 := base64.RawURLEncoding.EncodeToString
	unsigned := b64([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + b64([]byte(claims))
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", "", err
	}
	return aud, unsigned + "." + b64(sig), nil
}

// saString reads a string field of the account: absent (has false), null (isNull), or a string; another type is an
// error (the JS would convert it with String(); no real service account has one).
func saString(sa map[string]json.RawMessage, name string) (s string, has, isNull bool, err error) {
	v, ok := sa[name]
	if !ok {
		return "", false, false, nil
	}
	if string(v) == "null" {
		return "", false, true, nil
	}
	if json.Unmarshal(v, &s) != nil {
		return "", false, false, formError("the service account's " + name + " is not a string")
	}
	return s, true, false, nil
}

func saKey(v json.RawMessage) (*rsa.PrivateKey, error) {
	bad := formError("the service account's private_key is not an RSA private key in PEM")
	var text string
	if json.Unmarshal(v, &text) != nil {
		return nil, bad
	}
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, bad
	}
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if rk, ok := k.(*rsa.PrivateKey); err == nil && ok {
			return rk, nil
		}
	case "RSA PRIVATE KEY":
		if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return k, nil
		}
	}
	return nil, bad
}
