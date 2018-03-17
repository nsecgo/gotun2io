package tun2websocket

import (
	_ "net/http/pprof"
	"log"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/gorilla/websocket"
	"io"
	"github.com/nsecgo/gotun2io"
	"net"
	"sync"
	"sync/atomic"
	"errors"
	"bytes"
	"net/http"
	"flag"
	"encoding/binary"
)

const (
	connecting        byte = iota
	connectionSuccess
	establish
	closed
)

var (
	ErrConnFail = errors.New("connection fail")
)

type wsConn struct {
	*websocket.Conn
	endpoints sync.Map
	id        uint32
	writeChan chan []byte
}

func newWsConn(url string) *wsConn {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	conn.UnderlyingConn().(*net.TCPConn).SetKeepAlive(true)
	conn.UnderlyingConn().(*net.TCPConn).SetKeepAlivePeriod(time.Minute)
	conn.UnderlyingConn().(*net.TCPConn).SetNoDelay(false)
	var id uint32
	var eps sync.Map
	wrtChan := make(chan []byte, 1000)
	return &wsConn{conn, eps, id, wrtChan}
}
func (wc *wsConn) mainLoop() {
	go func() {
		for {
			if err := wc.WriteMessage(websocket.BinaryMessage, <-wc.writeChan); err != nil {
				log.Println(err)
				break
			}
		}
	}()
	go func() {
		var p []byte
		var err error
		for {
			_, p, err = wc.ReadMessage()
			if err != nil {
				log.Println(err)
				break
			}

			// dispatch
			ep, ok := wc.endpoints.Load(binary.BigEndian.Uint32(p[:4]))
			if !ok {
				continue
			}
			if p[4] == establish {
				ep.(*endpoint).writer.Write(p[5:])
			} else if p[4] == connectionSuccess {
				ep.(*endpoint).writer.Write([]byte{connectionSuccess})
			} else if p[4] == closed {
				ep.(*endpoint).discard()
			}
		}
	}()
}

func (wc *wsConn) Dial(network, address string) (io.ReadWriteCloser, error) {
	var ep = new(endpoint)
	ep.wsConn = wc
	ep.reader, ep.writer = io.Pipe()
	var id uint32
	for {
		id = atomic.AddUint32(&wc.id, 1)
		if _, ok := wc.endpoints.LoadOrStore(id, ep); !ok {
			break
		}
	}
	var p = make([]byte, 4)
	binary.BigEndian.PutUint32(p, id)
	ep.id = id
	ep.sid = p
	p = append(p, connecting)
	wc.writeChan <- append(p, []byte(network+"://"+address)...)

	if n, err := ep.reader.Read(p); n == 1 && err == nil && p[0] == connectionSuccess {
		return ep, nil
	}
	return nil, ErrConnFail
}

type endpoint struct {
	*wsConn
	id     uint32
	sid    []byte
	reader *io.PipeReader
	writer *io.PipeWriter
}

func (ep *endpoint) discard() {
	ep.writer.Close()
	ep.wsConn.endpoints.Delete(ep.id)
}
func (ep *endpoint) Read(p []byte) (n int, err error) {
	return ep.reader.Read(p)
}
func (ep *endpoint) Write(p []byte) (n int, err error) {
	ep.writeChan <- bytes.Join([][]byte{ep.sid, {establish}, p}, nil)
	return len(p), nil
}
func (ep *endpoint) Close() error {
	ep.writeChan <- bytes.Join([][]byte{ep.sid, {closed}}, nil)
	ep.discard()
	return nil
}

func goClient(addr string) {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	wc := newWsConn(addr)
	defer wc.Close()

	gotun2io.Run(wc)
	wc.mainLoop()

	select {
	case <-interrupt:
		log.Println("interrupt")

		// Cleanly close the connection by sending a close message and then
		// waiting (with timeout) for the server to close the connection.
		err := wc.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(3*time.Second))
		if err != nil {
			log.Fatal("write close:", err)
		}
		select {
		case <-time.After(time.Second):
		}
		os.Exit(1)
	}
}
func goServer(pwd string) {
	var upgrader = websocket.Upgrader{} // use default options
	http.HandleFunc("/"+pwd, func(w http.ResponseWriter, r *http.Request) {
		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Print("upgrade:", err)
			return
		}
		defer wsConn.Close()
		wsConn.UnderlyingConn().(*net.TCPConn).SetNoDelay(false)
		wsConn.UnderlyingConn().(*net.TCPConn).SetKeepAlive(true)
		wsConn.UnderlyingConn().(*net.TCPConn).SetKeepAlivePeriod(time.Minute)

		var endpoints sync.Map
		var writeChan = make(chan []byte)
		go func() {
			var err error
			for {
				err = wsConn.WriteMessage(websocket.BinaryMessage, <-writeChan)
				if err != nil {
					log.Println(err)
					break
				}
			}
		}()
		var wsRead []byte
		for {
			_, wsRead, err = wsConn.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				break
			}
			if wsRead[4] == connecting {
				u, err := url.Parse(string(wsRead[5:]))
				if err != nil {
					log.Println(err)
					continue
				}
				conn, err := net.Dial(u.Scheme, u.Host)
				if err != nil {
					log.Println("dial:", u.Scheme, u.Host, err)
					wsRead[4] = closed
					writeChan <- wsRead[:5]
					continue
				}
				wsRead[4] = connectionSuccess
				writeChan <- wsRead[:5]

				endpoints.Store(binary.BigEndian.Uint32(wsRead[:4]), conn)

				go func(id []byte) {
					buf := make([]byte, 1500)
					buf[0] = id[0]
					buf[1] = id[1]
					buf[2] = id[2]
					buf[3] = id[3]
					buf[4] = establish
					for {
						n, err := conn.Read(buf[5:])
						if err != nil {
							if err == io.EOF && conn.Close() == nil {
								buf[4] = closed
								writeChan <- buf[:5]
							}
							endpoints.Delete(binary.BigEndian.Uint32(buf[:4]))
							break
						}
						writeChan <- buf[:5+n]
					}
				}(wsRead[:4])
			} else if wsRead[4] == establish {
				conn, ok := endpoints.Load(binary.BigEndian.Uint32(wsRead[:4]))
				if !ok {
					wsRead[4] = closed
					writeChan <- wsRead[:5]
					continue
				}
				_, err = conn.(net.Conn).Write(wsRead[5:])
				if err != nil {
					//conn.(net.Conn).Close()
					log.Println("vps写错误", err)
				}
			} else if wsRead[4] == closed {
				conn, ok := endpoints.Load(binary.BigEndian.Uint32(wsRead[:4]))
				if ok {
					conn.(net.Conn).Close()
				}
			}
		}
	})
}
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	var c = flag.Bool("c", false, "as a client")
	var s = flag.Bool("s", false, "as a server")
	var addr = flag.String("addr", "", "http service address")
	var pwd = flag.String("pwd", "1234", "http service pwd")
	var listen = flag.String("l", ":8080", "")
	flag.Parse()

	if *c {
		go goClient(*addr)
	} else if *s {
		goServer(*pwd)
	}
	http.ListenAndServe(*listen, nil)
}
