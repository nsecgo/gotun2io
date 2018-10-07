package main

import (
	"flag"
	"github.com/nsecgo/gotun2io"
	"golang.org/x/net/proxy"
	"io"
	"log"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	var proxyAddr = flag.String("proxy", "127.0.0.1:1080", "address of socks5 proxy")
	flag.Parse()
	dialer, err := proxy.SOCKS5("tcp", *proxyAddr, nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	gotun2io.Run(func(network, address string) (io.ReadWriteCloser, error) {
		return dialer.Dial(network, address)
	})
	select {}
}
