//go:build darwin && arm64

package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

const (
	tunReplyLimit = 4096
	tunStartLimit = 30 * time.Second
	tunStopLimit  = 12 * time.Second
)

// One open connection owns the routes. The first reply acknowledges startup,
// and the second reports teardown. EOF also requests teardown.
type darwinTUNLease struct {
	conn *net.UnixConn
	done chan struct{}
	err  error // Written before done closes.
}

func readTUNFrame(r io.Reader, limit uint32) ([]byte, error) {
	var size uint32
	if err := binary.Read(r, binary.BigEndian, &size); err != nil {
		return nil, err
	}
	if size > limit {
		return nil, errors.New("TUN 消息超出大小限制")
	}
	body := make([]byte, size)
	_, err := io.ReadFull(r, body)
	return body, err
}

func writeTUNFrame(w io.Writer, body []byte) error {
	if err := binary.Write(w, binary.BigEndian, uint32(len(body))); err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	_, err := w.Write(body)
	return err
}

func readTUNReply(r io.Reader) error {
	body, err := readTUNFrame(r, tunReplyLimit)
	if err != nil {
		return errors.New("TUN 后台服务的连接已中断")
	}
	if len(body) != 0 {
		return errors.New(string(body))
	}
	return nil
}

func openDarwinTUN(ctx context.Context, body []byte) (_ *darwinTUNLease, result error) {
	if len(body) == 0 || len(body) > profileLimit {
		return nil, errors.New("TUN 订阅配置无效或超出大小限制")
	}
	ctx, cancel := context.WithTimeout(ctx, tunStartLimit)
	defer cancel()
	conn, err := requestDarwinTUNChannel()
	if err != nil {
		return nil, err
	}
	defer func() {
		if result != nil {
			_ = conn.Close()
		}
	}()
	interrupt := context.AfterFunc(ctx, func() { _ = conn.Close() })
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	if err = readTUNReply(conn); err != nil {
		return nil, err
	}
	if err = writeTUNFrame(conn, body); err != nil {
		return nil, errors.New("无法向 TUN 后台服务发送订阅配置")
	}
	if err = readTUNReply(conn); err != nil {
		return nil, err
	}
	interrupt()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	lease := &darwinTUNLease{conn: conn, done: make(chan struct{})}
	go func() {
		defer close(lease.done)
		defer conn.Close()
		lease.err = readTUNReply(conn)
	}()
	return lease, nil
}

func (l *darwinTUNLease) stop() error {
	defer l.conn.Close()
	// A half-close requests shutdown without losing the final route report.
	_ = l.conn.CloseWrite()
	timer := time.NewTimer(tunStopLimit)
	defer timer.Stop()
	select {
	case <-l.done:
		return l.err
	case <-timer.C:
		return errors.New("TUN 后台服务未能及时确认路由恢复")
	}
}
