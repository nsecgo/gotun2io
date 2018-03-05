package main

import (
	"flag"
	"net/http"
	"github.com/nsecgo/gotun2io"
)

func main() {
	socks5Addr := flag.String("socks5Addr", "127.0.0.1:1080", "")
	flag.Parse()
	go gotun2io.Run(*socks5Addr)
	http.ListenAndServe(":9000", nil)
}
