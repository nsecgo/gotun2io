package gotun2io

import (
	"github.com/google/netstack/tcpip/adapters/gonet"
	"github.com/google/netstack/tcpip/link/fdbased"
	"github.com/google/netstack/tcpip/link/rawfile"
	"github.com/google/netstack/tcpip/network/arp"
	"github.com/google/netstack/tcpip/network/ipv4"
	"github.com/google/netstack/tcpip/network/ipv6"
	"github.com/google/netstack/tcpip/stack"
	"github.com/google/netstack/tcpip/transport/tcp"
	"github.com/google/netstack/waiter"
	"github.com/nsecgo/water"
	"io"
	"log"
	"math/rand"
	"net"
	"strconv"
	"time"
)

const NICID = 666

// Non-Blocking run.
func Run(dial func(network, address string) (io.ReadWriteCloser, error)) {
	rand.Seed(time.Now().UnixNano())
	// Create the stack with ip and tcp protocols, then add a tun-based
	// NIC and address.
	s := stack.New([]string{ipv4.ProtocolName, ipv6.ProtocolName, arp.ProtocolName}, []string{tcp.ProtocolName}, stack.Options{})
	ifc, err := water.New(water.Config{
		DeviceType: water.TUN,
	})
	if err != nil {
		log.Fatal(err)
	}
	mtu, err := rawfile.GetMTU(ifc.Name())
	if err != nil {
		log.Fatal(err)
	}
	linkID := fdbased.New(&fdbased.Options{
		FD:  ifc.Fd,
		MTU: mtu,
	})
	if err := s.CreateNIC(NICID, linkID); err != nil {
		log.Fatal(err)
	}
	//addr := tcpip.Address(net.ParseIP("192.168.2.100").To4())
	//if err := s.AddAddress(NICID, ipv4.ProtocolNumber, addr); err != nil {
	//	log.Fatal(err)
	//}
	//if err := s.AddAddress(NICID, arp.ProtocolNumber, arp.ProtocolAddress); err != nil {
	//	log.Fatal(err)
	//}
	//// Add default route.
	//s.SetRouteTable([]tcpip.Route{
	//	{
	//		Destination: tcpip.Address(strings.Repeat("\x00", len(addr))),
	//		Mask:        tcpip.AddressMask(strings.Repeat("\x00", len(addr))),
	//		Gateway:     "",
	//		NIC:         NICID,
	//	},
	//})
	s.SetPromiscuousMode(NICID, true)

	var wq waiter.Queue
	fwd := tcp.NewForwarder(s, 0, 10, func(r *tcp.ForwarderRequest) {
		ep, er := r.CreateEndpoint(&wq)
		if er != nil {
			transportEndpointID := r.ID()
			log.Println(er, net.JoinHostPort(transportEndpointID.LocalAddress.String(), strconv.Itoa(int(transportEndpointID.LocalPort))))
			r.Complete(false)
			return
		}
		defer ep.Close()
		transportEndpointID := r.ID()
		r.Complete(false)

		conn, err := dial("tcp", net.JoinHostPort(transportEndpointID.LocalAddress.String(), strconv.Itoa(int(transportEndpointID.LocalPort))))
		if err != nil {
			log.Println(err)
			return
		}
		defer conn.Close()
		fwdConn := gonet.NewConn(&wq, ep)
		go io.Copy(fwdConn, conn)
		io.Copy(conn, fwdConn)
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
}
