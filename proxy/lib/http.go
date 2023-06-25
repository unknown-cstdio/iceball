package snowflake_proxy

import (
	"net/http"
)

// Implements the http.Handler interface
type ProxyHandler struct {
	//TODO: add necessary fields
	handle func(http.ResponseWriter, *http.Request)
}

func (ph ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Origin, X-Session-ID")
	ph.handle(w, r)
}

func AddClient(w http.ResponseWriter, r *http.Request) {
}

func TransferClient(w http.ResponseWriter, r *http.Request) {
}

func ConnectClient(w http.ResponseWriter, r *http.Request) {
}
