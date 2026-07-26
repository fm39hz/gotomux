package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/daemon"
)

func main() {
	d, err := daemon.New(config.Load())
	if err != nil {
		fmt.Fprintf(os.Stderr, "gotomuxd: %v\n", err)
		os.Exit(1)
	}

	// Shut down on SIGTERM/SIGINT. Without this, `systemctl stop` or a restart
	// killed the process outright: Close never ran, so the store was never
	// closed (WAL left without a checkpoint), the control client was never
	// reaped, and the socket file was always left behind — which made the
	// stale-socket recovery path in listenWithGuard the normal startup path
	// rather than an exceptional one.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	stopped := make(chan struct{})
	go func() {
		s := <-sig
		log.Printf("received %v — shutting down", s)
		d.Shutdown()
		close(stopped)
	}()

	log.Println("listening")
	err = daemon.ServeIPC(d)
	select {
	case <-stopped:
		// Accept failed because Shutdown closed the listener; that is success.
		d.Close()
		return
	default:
	}

	d.Close()
	if err != nil && !errors.Is(err, net.ErrClosed) {
		log.Fatal(err)
	}
}
