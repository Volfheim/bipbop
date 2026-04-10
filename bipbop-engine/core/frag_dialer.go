package core

import (
	"net"
	"time"
)

// FragDialer implements a net.Dialer that forces 1-byte TCP fragmentation 
// of the initial data (TLS ClientHello) to bypass SNI-based DPI filtering.
type FragDialer struct {
	net.Dialer
}

func (d *FragDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &fragConn{Conn: conn}, nil
}

type fragConn struct {
	net.Conn
	wasFragmented bool
}

func (c *fragConn) Write(b []byte) (n int, err error) {
	// Мы фрагментируем только САМЫЙ ПЕРВЫЙ пакет (обычно это TLS ClientHello),
	// чтобы обмануть DPI, который ищет SNI (имя домена).
	if !c.wasFragmented && len(b) > 5 {
		c.wasFragmented = true
		
		// 1. Отправляем первый байт отдельно
		n1, err := c.Conn.Write(b[:1])
		if err != nil {
			return n1, err
		}
		
		// 2. Небольшая пауза, чтобы пакеты не склеились в стеке (зависит от ОС)
		time.Sleep(1 * time.Millisecond)

		// 3. Отправляем еще пару байт
		n2, err := c.Conn.Write(b[1:3])
		if err != nil {
			return n1 + n2, err
		}

		// 4. Отправляем всё остальное
		n3, err := c.Conn.Write(b[3:])
		return n1 + n2 + n3, err
	}
	
	return c.Conn.Write(b)
}
