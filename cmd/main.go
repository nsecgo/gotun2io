package main

import (
	_ "net/http/pprof"
	"log"
	"github.com/google/netstack/tcpip"
	"github.com/google/netstack/tcpip/link/fdbased"
	"github.com/google/netstack/tcpip/link/rawfile"
	"github.com/google/netstack/tcpip/link/tun"
	"github.com/google/netstack/tcpip/network/ipv4"
	"github.com/google/netstack/tcpip/network/ipv6"
	"github.com/google/netstack/tcpip/stack"
	"github.com/google/netstack/tcpip/transport/tcp"
	"github.com/google/netstack/waiter"
	"golang.org/x/net/proxy"
	"net"
	"fmt"
	"os"
	"net/http"
)

const NICID = 1

func init() {
	log.SetFlags(log.Lshortfile | log.LstdFlags)
}

func run() {
	if len(os.Args) != 2 {
		log.Fatal("Usage: ", os.Args[0], " <tun-device>")
	}

	tunName := os.Args[1]

	// Create the stack with ip and tcp protocols, then add a tun-based NIC and address.
	s := stack.New([]string{ipv4.ProtocolName, ipv6.ProtocolName}, []string{tcp.ProtocolName})

	mtu, err := rawfile.GetMTU(tunName)
	if err != nil {
		log.Fatal(err)
	}

	fd, err := tun.Open(tunName)
	if err != nil {
		log.Fatal(err)
	}

	linkID := fdbased.New(fd, mtu, nil)
	if err := s.CreateNIC(NICID, linkID); err != nil {
		log.Fatal(err)
	}

	s.SetPromiscuousMode(NICID, true)

	dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:1080", nil, &net.Dialer{})
	if err != nil {
		log.Fatal(err)
	}

	var wq waiter.Queue
	fwd := tcp.NewForwarder(s, 0, 10, func(r *tcp.ForwarderRequest) {
		ep, er := r.CreateEndpoint(&wq)
		if er != nil {
			log.Fatal(er)
		}
		defer ep.Close()
		transportEndpointID := r.ID()
		r.Complete(false)

		conn, err := dialer.Dial("tcp", fmt.Sprintf("%v:%d", transportEndpointID.LocalAddress, transportEndpointID.LocalPort))
		if err != nil {
			log.Fatal(err)
		}
		socksConn := conn.(*net.TCPConn)

		// Create wait queue entry that notifies a channel.
		waitEntry, notifyCh := waiter.NewChannelEntry(nil)
		wq.EventRegister(&waitEntry, waiter.EventIn)
		defer wq.EventUnregister(&waitEntry)

		go func() {
			buf := make([]byte, 1500)
			for {
				n, err := socksConn.Read(buf)
				if err != nil {
					return
				}
				ep.Write(buf[:n], nil)
			}
		}()
		for {
			v, err := ep.Read(nil)
			if err != nil {
				if err == tcpip.ErrWouldBlock {
					<-notifyCh
					continue
				}
				return
			}
			socksConn.Write(v)
		}
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
}
func main() {
	go run()
	http.ListenAndServe(":9000", nil)
}
