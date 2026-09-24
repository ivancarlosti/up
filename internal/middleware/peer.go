package middleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/services"
)

// maxPeerBodyBytes caps the body read while verifying a peer request. Peer
// payloads are small (a ping, a status list) and a larger one is a mistake or an
// attack, so it is rejected before being buffered.
const maxPeerBodyBytes = 1 << 20 // 1 MiB

// errBodyTooLarge is returned by readPeerBody when the cap is exceeded.
var errBodyTooLarge = errors.New("peer request body too large")

// PeerAuthenticator is the part of the cluster service the peer middleware needs.
// It is an interface so the middleware can be tested without a database.
type PeerAuthenticator interface {
	AuthenticatePeer(ctx context.Context, in services.PeerSignatureInput, presented string) error
}

// RequirePeerKey authenticates an inbound node to node request: the HMAC
// signature over the canonical request, a timestamp inside the window and a nonce
// that has not been used before (docs/clustering-modes.md, section 11).
//
// The routes it protects are deliberately not covered by the IP rules: a peer is
// not a dashboard visitor, and the signature is the control.
func RequirePeerKey(auth PeerAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := readPeerBody(c)
		if err != nil {
			api.WriteError(c, http.StatusRequestEntityTooLarge, i18n.CodeInvalidPayload,
				"the peer request body is too large")
			return
		}

		in := services.PeerSignatureInput{
			Method:    c.Request.Method,
			Target:    c.Request.URL.RequestURI(),
			Body:      body,
			Timestamp: c.GetHeader(services.HeaderPeerTimestamp),
			Nonce:     c.GetHeader(services.HeaderPeerNonce),
			NodeID:    c.GetHeader(services.HeaderPeerNode),
		}

		// The rejection reason is logged server side by the service; the response
		// stays deliberately vague.
		if err := auth.AuthenticatePeer(c.Request.Context(), in, c.GetHeader(services.HeaderPeerSignature)); err != nil {
			api.Forbidden(c, i18n.CodeClusterKeyInvalid, "peer authentication failed")
			return
		}

		c.Set(CtxPeerNode, in.NodeID)
		c.Next()
	}
}

// readPeerBody buffers the body so it can be hashed and still be read by the
// handler (gin offers no rewindable body).
func readPeerBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxPeerBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxPeerBodyBytes {
		return nil, errBodyTooLarge
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
