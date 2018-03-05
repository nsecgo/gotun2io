package gotun2io

import (
	_ "net/http/pprof"
	"log"
	"github.com/google/netstack/tcpip"
	"github.com/google/netstack/tcpip/link/fdbased"
	"github.com/google/netstack/tcpip/link/rawfile"
	"github.com/google/netstack/tcpip/network/ipv4"
	"github.com/google/netstack/tcpip/network/ipv6"
	"github.com/google/netstack/tcpip/stack"
	"github.com/google/netstack/tcpip/transport/tcp"
	"github.com/google/netstack/waiter"
	"golang.org/x/net/proxy"
	"net"
	"fmt"
	"github.com/nsecgo/water"
)

const NICID = 666

func Run(socks5Addr string) {
	// Create the stack with ip and tcp protocols, then add a tun-based NIC and address.
	s := stack.New([]string{ipv4.ProtocolName, ipv6.ProtocolName}, []string{tcp.ProtocolName})

	ifce, err := water.New(water.Config{
		DeviceType: water.TUN,
	})
	if err != nil {
		log.Fatal(err)
	}
	mtu, err := rawfile.GetMTU(ifce.Name())
	if err != nil {
		log.Fatal(err)
	}

	linkID := fdbased.New(ifce.Fd, mtu, nil)
	if err := s.CreateNIC(NICID, linkID); err != nil {
		log.Fatal(err)
	}

	s.SetPromiscuousMode(NICID, true)

	dialer, err := proxy.SOCKS5("tcp", socks5Addr, nil, &net.Dialer{})
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

		socksConn, err := dialer.Dial("tcp", fmt.Sprintf("%v:%d", transportEndpointID.LocalAddress, transportEndpointID.LocalPort))
		if err != nil {
			log.Fatal(err)
		}
		socksConn.(*net.TCPConn).SetNoDelay(false)

		// Create wait queue entry that notifies a channel.
		waitEntry, notifyCh := waiter.NewChannelEntry(nil)
		wq.EventRegister(&waitEntry, waiter.EventIn)
		defer wq.EventUnregister(&waitEntry)

		go func() {
			buf := make([]byte, mtu)
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
