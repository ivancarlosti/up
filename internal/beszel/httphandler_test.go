package beszel

import (
	"net/http"

	"github.com/gorilla/websocket"
)

// httpHandler adapts a raw websocket session to an http.Handler, so a test can
// own the socket without a fake protocol on top of it.
func httpHandler(session func(*websocket.Conn)) http.Handler {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		session(ws)
	})
}
