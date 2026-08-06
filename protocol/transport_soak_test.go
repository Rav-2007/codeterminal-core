package protocol

import (
	"net"
	"sync"
	"testing"
)

// TestTransportTCP_Soak tests rapid bind/dial cycles on the TCP loopback transport.
// This validates that no socket leaks or ECONNREFUSED races occur during rapid
// daemon/client spin-ups, satisfying the cross-platform reliability requirement
// for Windows TCP transports.
func TestTransportTCP_Soak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping soak test in short mode")
	}

	const iterations = 500
	var wg sync.WaitGroup

	for i := 0; i < iterations; i++ {
		addr := Address{Transport: TransportTCP, Address: "127.0.0.1:0"}
		ln, err := Listen(addr)
		if err != nil {
			t.Fatalf("Listen failed on iter %d: %v", i, err)
		}

		addr.Address = ln.Addr().String()
		wg.Add(1)

		// Daemon spin-up simulator
		go func(l net.Listener) {
			defer wg.Done()
			conn, err := l.Accept()
			if err == nil {
				conn.Close()
			}
		}(ln)

		// Client spin-up simulator
		conn, err := Dial(addr)
		if err != nil {
			ln.Close()
			t.Fatalf("Dial failed on iter %d: %v", i, err)
		}
		conn.Close()
		ln.Close()
	}

	wg.Wait()
}
