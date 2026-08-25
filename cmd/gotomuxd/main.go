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
	//
	// SIGHUP reloads the config instead. The daemon runs all day and losing the
	// in-memory caches plus a few telemetry cycles to restart for a single
	// number is not a fair trade; config.Load normalizes and degrades to
	// defaults on a malformed file, so a broken edit can never kill a healthy
	// daemon. A reload deliberately cannot touch the socket path, the store
	// handle or the control session — those bind once at startup.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	stopped := make(chan struct{})
	go func() {
		for s := range sig {
			if s != syscall.SIGHUP {
				log.Printf("received %v — shutting down", s)
				d.Shutdown()
				close(stopped)
				return
			}
			d.ReloadConfig(config.Load())
		}
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
	switch {
	case err == nil, errors.Is(err, net.ErrClosed):
		// Clean shutdown.
	case errors.Is(err, daemon.ErrAlreadyRunning):
		// Another instance is serving, so there is nothing to do and nothing has
		// gone wrong. Exiting zero matters: with Restart=on-failure, treating this
		// as an error makes systemd relaunch forever against a lock it can never
		// take.
		log.Printf("%v — exiting; the existing instance keeps serving", err)
	default:
		log.Fatal(err)
	}
}
