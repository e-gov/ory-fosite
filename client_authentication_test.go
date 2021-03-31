/*
 * Copyright © 2017-2018 Aeneas Rekkas <aeneas+oss@aeneas.io>
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * @author		Aeneas Rekkas <aeneas+oss@aeneas.io>
 * @Copyright 	2017-2018 Aeneas Rekkas <aeneas+oss@aeneas.io>
 * @license 	Apache-2.0
 *
 */

package fosite_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ory/x/errorsx"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite/token/jwt"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	jose "gopkg.in/square/go-jose.v2"

	. "github.com/ory/fosite"
	"github.com/ory/fosite/internal"
	"github.com/ory/fosite/storage"
)

func mustGenerateRSAAssertion(t *testing.T, claims jwt.MapClaims, key *rsa.PrivateKey, kid string) string {
	token := jwt.NewWithClaims(jose.RS256, claims)
	token.Header["kid"] = kid
	tokenString, err := token.SignedString(key)
	require.NoError(t, err)
	return tokenString
}

func mustGenerateECDSAAssertion(t *testing.T, claims jwt.MapClaims, key *ecdsa.PrivateKey, kid string) string {
	token := jwt.NewWithClaims(jose.ES256, claims)
	token.Header["kid"] = kid
	tokenString, err := token.SignedString(key)
	require.NoError(t, err)
	return tokenString
}

func mustGenerateHSAssertion(t *testing.T, claims jwt.MapClaims, key *rsa.PrivateKey, kid string) string {
	token := jwt.NewWithClaims(jose.HS256, claims)
	tokenString, err := token.SignedString([]byte("aaaaaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbbbbcccccccccccccccccccccddddddddddddddddddddddd"))
	require.NoError(t, err)
	return tokenString
}

func mustGenerateNoneAssertion(t *testing.T, claims jwt.MapClaims, key *rsa.PrivateKey, kid string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tokenString, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	return tokenString
}

// returns an http basic authorization header, encoded using application/x-www-form-urlencoded
func clientBasicAuthHeader(clientID, clientSecret string) http.Header {
	creds := url.QueryEscape(clientID) + ":" + url.QueryEscape(clientSecret)
	return http.Header{
		"Authorization": {
			"Basic " + base64.StdEncoding.EncodeToString([]byte(creds)),
		},
	}
}

func TestAuthenticateClient(t *testing.T) {
	const at = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

	var hashClientSecret = func(clientSecret []byte) ([]byte, error) {
		var err error
		hashedClientSecret := sha256.New()
		_, err = hashedClientSecret.Write(clientSecret)
		if err != nil {
			return nil, errorsx.WithStack(ErrInvalidClient.WithWrap(err).WithDebug(err.Error()))
		}
		sha256Hash := hex.EncodeToString(hashedClientSecret.Sum(nil))
		return []byte(sha256Hash), nil
	}

	hasher := &BCrypt{WorkFactor: 6}
	f := &Fosite{
		JWKSFetcherStrategy: NewDefaultJWKSFetcherStrategy(),
		Store:               storage.NewMemoryStore(),
		Hasher:              hasher,
		TokenURL:            "token-url",
	}

	barSecretHash, err := hashClientSecret([]byte("bar"))
	require.NoError(t, err)
	barSecret, err := hasher.Hash(context.TODO(), barSecretHash)
	require.NoError(t, err)

	// a secret containing various special characters
	complexSecretRaw := "foo %66%6F%6F@$<§!✓"
	complexSecretHash, err := hashClientSecret([]byte(complexSecretRaw))
	require.NoError(t, err)
	complexSecret, err := hasher.Hash(context.TODO(), complexSecretHash)
	require.NoError(t, err)

	rsaKey := internal.MustRSAKey()
	rsaJwks := &jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{
			{
				KeyID: "kid-foo",
				Use:   "sig",
				Key:   &rsaKey.PublicKey,
			},
		},
	}

	ecdsaKey := internal.MustECDSAKey()
	ecdsaJwks := &jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{
			{
				KeyID: "kid-foo",
				Use:   "sig",
				Key:   &ecdsaKey.PublicKey,
			},
		},
	}

	var h http.HandlerFunc
	h = func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(rsaJwks))
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	for k, tc := range []struct {
		d             string
		client        *DefaultOpenIDConnectClient
		assertionType string
		assertion     string
		r             *http.Request
		form          url.Values
		expectErr     error
	}{
		{
			d:         "should fail because authentication can not be determined",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo"}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:      url.Values{},
			r:         new(http.Request),
			expectErr: ErrInvalidRequest,
		},
		{
			d:         "should fail because client does not exist",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Public: true}, TokenEndpointAuthMethod: "none"},
			form:      url.Values{"client_id": []string{"bar"}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should pass because client is public and authentication requirements are met",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Public: true}, TokenEndpointAuthMethod: "none"},
			form:   url.Values{"client_id": []string{"foo"}},
			r:      new(http.Request),
		},
		{
			d:      "should pass because client is public and client secret is empty in query param",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Public: true}, TokenEndpointAuthMethod: "none"},
			form:   url.Values{"client_id": []string{"foo"}, "client_secret": []string{""}},
			r:      new(http.Request),
		},
		{
			d:      "should pass because client is public and client secret is empty in basic auth header",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Public: true}, TokenEndpointAuthMethod: "none"},
			form:   url.Values{},
			r:      &http.Request{Header: clientBasicAuthHeader("foo", "")},
		},
		{
			d:         "should fail because client requires basic auth and client secret is empty in basic auth header",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Public: true}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:      url.Values{},
			r:         &http.Request{Header: clientBasicAuthHeader("foo", "")},
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should pass with client credentials containing special characters",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "!foo%20bar", Secret: complexSecret}, TokenEndpointAuthMethod: "client_secret_post"},
			form:   url.Values{"client_id": []string{"!foo%20bar"}, "client_secret": []string{complexSecretRaw}},
			r:      new(http.Request),
		},
		{
			d:      "should pass with client credentials containing special characters via basic auth",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo — bar! +<&>*", Secret: complexSecret}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:   url.Values{},
			r:      &http.Request{Header: clientBasicAuthHeader("foo — bar! +<&>*", complexSecretRaw)},
		},
		{
			d:         "should fail because auth method is not none",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Public: true}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:      url.Values{"client_id": []string{"foo"}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should pass because client is confidential and id and secret match in post body",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: []byte("invalid_hash"), RotatedSecrets: [][]byte{barSecret}}, TokenEndpointAuthMethod: "client_secret_post"},
			form:   url.Values{"client_id": []string{"foo"}, "client_secret": []string{"bar"}},
			r:      new(http.Request),
		},
		{
			d:      "should pass because client is confidential and id and rotated secret match in post body",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret}, TokenEndpointAuthMethod: "client_secret_post"},
			form:   url.Values{"client_id": []string{"foo"}, "client_secret": []string{"bar"}},
			r:      new(http.Request),
		},
		{
			d:         "should fail because client is confidential and secret does not match in post body",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret}, TokenEndpointAuthMethod: "client_secret_post"},
			form:      url.Values{"client_id": []string{"foo"}, "client_secret": []string{"baz"}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:         "should fail because client is confidential and id does not exist in post body",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, TokenEndpointAuthMethod: "client_secret_post"},
			form:      url.Values{"client_id": []string{"foo"}, "client_secret": []string{"bar"}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should pass because client is confidential and id and secret match in header",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:   url.Values{},
			r:      &http.Request{Header: clientBasicAuthHeader("foo", "bar")},
		},
		{
			d:      "should pass because client is confidential and id and rotated secret match in header",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: []byte("invalid_hash"), RotatedSecrets: [][]byte{barSecret}}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:   url.Values{},
			r:      &http.Request{Header: clientBasicAuthHeader("foo", "bar")},
		},
		{
			d:      "should pass because client is confidential and id and rotated secret match in header",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: []byte("invalid_hash"), RotatedSecrets: [][]byte{[]byte("invalid"), barSecret}}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:   url.Values{},
			r:      &http.Request{Header: clientBasicAuthHeader("foo", "bar")},
		},
		{
			d:         "should fail because auth method is not client_secret_basic",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret}, TokenEndpointAuthMethod: "client_secret_post"},
			form:      url.Values{},
			r:         &http.Request{Header: clientBasicAuthHeader("foo", "bar")},
			expectErr: ErrInvalidClient,
		},
		{
			d:         "should fail because client is confidential and secret does not match in header",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:      url.Values{},
			r:         &http.Request{Header: clientBasicAuthHeader("foo", "baz")},
			expectErr: ErrInvalidClient,
		},
		{
			d:         "should fail because client is confidential and neither secret nor rotated does match in header",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret, RotatedSecrets: [][]byte{barSecret}}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:      url.Values{},
			r:         &http.Request{Header: clientBasicAuthHeader("foo", "baz")},
			expectErr: ErrInvalidClient,
		},
		{
			d:         "should fail because client id is not encoded using application/x-www-form-urlencoded",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:      url.Values{},
			r:         &http.Request{Header: http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("%%%%%%:foo"))}}},
			expectErr: ErrInvalidRequest,
		},
		{
			d:         "should fail because client secret is not encoded using application/x-www-form-urlencoded",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:      url.Values{},
			r:         &http.Request{Header: http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("foo:%%%%%%%"))}}},
			expectErr: ErrInvalidRequest,
		},
		{
			d:         "should fail because client is confidential and id does not exist in header",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, TokenEndpointAuthMethod: "client_secret_basic"},
			form:      url.Values{},
			r:         &http.Request{Header: http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("foo:bar"))}}},
			expectErr: ErrInvalidClient,
		},
		{
			d:         "should fail because client_assertion but client_assertion is missing",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret}, TokenEndpointAuthMethod: "private_key_jwt"},
			form:      url.Values{"client_id": []string{"foo"}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidRequest,
		},
		{
			d:         "should fail because client_assertion_type is unknown",
			client:    &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "foo", Secret: barSecret}, TokenEndpointAuthMethod: "private_key_jwt"},
			form:      url.Values{"client_id": []string{"foo"}, "client_assertion_type": []string{"foobar"}},
			r:         new(http.Request),
			expectErr: ErrInvalidRequest,
		},
		{
			d:      "should pass with proper RSA assertion when JWKs are set within the client and client_id is not set in the request",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r: new(http.Request),
		},
		{
			d:      "should pass with proper ECDSA assertion when JWKs are set within the client and client_id is not set in the request",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: ecdsaJwks, TokenEndpointAuthMethod: "private_key_jwt", TokenEndpointAuthSigningAlgorithm: "ES256"},
			form: url.Values{"client_assertion": {mustGenerateECDSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, ecdsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r: new(http.Request),
		},
		{
			d:      "should fail because RSA assertion is used, but ECDSA assertion is required",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: ecdsaJwks, TokenEndpointAuthMethod: "private_key_jwt", TokenEndpointAuthSigningAlgorithm: "ES256"},
			form: url.Values{"client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should fail because token auth method is not private_key_jwt, but client_secret_jwt",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "client_secret_jwt"},
			form: url.Values{"client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should fail because token auth method is not private_key_jwt, but none",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "none"},
			form: url.Values{"client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should fail because token auth method is not private_key_jwt, but client_secret_post",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "client_secret_post"},
			form: url.Values{"client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should fail because token auth method is not private_key_jwt, but client_secret_basic",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "client_secret_basic"},
			form: url.Values{"client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should fail because token auth method is not private_key_jwt, but foobar",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "foobar"},
			form: url.Values{"client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should pass with proper assertion when JWKs are set within the client and client_id is not set in the request (aud is array)",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": []string{"token-url-2", "token-url"},
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r: new(http.Request),
		},
		{
			d:      "should fail because audience (array) does not match token url",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": []string{"token-url-1", "token-url-2"},
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should pass with proper assertion when JWKs are set within the client",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_id": []string{"bar"}, "client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r: new(http.Request),
		},
		{
			d:      "should fail because JWT algorithm is HS256",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_id": []string{"bar"}, "client_assertion": {mustGenerateHSAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should fail because JWT algorithm is none",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_id": []string{"bar"}, "client_assertion": {mustGenerateNoneAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should pass with proper assertion when JWKs URI is set",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeysURI: ts.URL, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_id": []string{"bar"}, "client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r: new(http.Request),
		},
		{
			d:      "should fail because client_assertion sub does not match client",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_id": []string{"bar"}, "client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "not-bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should fail because client_assertion iss does not match client",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_id": []string{"bar"}, "client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "not-bar",
				"jti": "12345",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should fail because client_assertion jti is not set",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_id": []string{"bar"}, "client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"aud": "token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
		{
			d:      "should fail because client_assertion aud is not set",
			client: &DefaultOpenIDConnectClient{DefaultClient: &DefaultClient{ID: "bar", Secret: barSecret}, JSONWebKeys: rsaJwks, TokenEndpointAuthMethod: "private_key_jwt"},
			form: url.Values{"client_id": []string{"bar"}, "client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
				"sub": "bar",
				"exp": time.Now().Add(time.Hour).Unix(),
				"iss": "bar",
				"jti": "12345",
				"aud": "not-token-url",
			}, rsaKey, "kid-foo")}, "client_assertion_type": []string{at}},
			r:         new(http.Request),
			expectErr: ErrInvalidClient,
		},
	} {
		t.Run(fmt.Sprintf("case=%d/description=%s", k, tc.d), func(t *testing.T) {
			store := storage.NewMemoryStore()
			store.Clients[tc.client.ID] = tc.client
			f.Store = store

			c, err := f.AuthenticateClient(nil, tc.r, tc.form)
			if tc.expectErr != nil {
				require.EqualError(t, err, tc.expectErr.Error())
				return
			}

			if err != nil {
				var validationError *jwt.ValidationError
				var rfcError *RFC6749Error
				if errors.As(err, &validationError) {
					t.Logf("Error is: %s", validationError.Inner)
				} else if errors.As(err, &rfcError) {
					t.Logf("DebugField is: %s", rfcError.DebugField)
					t.Logf("HintField is: %s", rfcError.HintField)
				}
			}
			require.NoError(t, err)
			assert.EqualValues(t, tc.client, c)
		})
	}
}

func TestAuthenticateClientTwice(t *testing.T) {
	const at = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

	key := internal.MustRSAKey()
	client := &DefaultOpenIDConnectClient{
		DefaultClient: &DefaultClient{
			ID:     "bar",
			Secret: []byte("secret"),
		},
		JSONWebKeys: &jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{
				{
					KeyID: "kid-foo",
					Use:   "sig",
					Key:   &key.PublicKey,
				},
			},
		},
		TokenEndpointAuthMethod: "private_key_jwt",
	}
	store := storage.NewMemoryStore()
	store.Clients[client.ID] = client

	hasher := &BCrypt{WorkFactor: 6}
	f := &Fosite{
		JWKSFetcherStrategy: NewDefaultJWKSFetcherStrategy(),
		Store:               store,
		Hasher:              hasher,
		TokenURL:            "token-url",
	}

	formValues := url.Values{"client_id": []string{"bar"}, "client_assertion": {mustGenerateRSAAssertion(t, jwt.MapClaims{
		"sub": "bar",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iss": "bar",
		"jti": "12345",
		"aud": "token-url",
	}, key, "kid-foo")}, "client_assertion_type": []string{at}}

	c, err := f.AuthenticateClient(nil, new(http.Request), formValues)
	require.NoError(t, err, "%#v", err)
	assert.Equal(t, client, c)

	// replay the request and expect it to fail
	c, err = f.AuthenticateClient(nil, new(http.Request), formValues)
	require.Error(t, err)
	assert.EqualError(t, err, ErrJTIKnown.Error())
	assert.Nil(t, c)
}
