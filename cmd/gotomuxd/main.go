package main

import (
	"fmt"
	"log"
	"os"

	"github.com/fm39hz/gotomux/internal/daemon"
)

func main() {
	d, err := daemon.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gotomuxd: %v\n", err)
		os.Exit(1)
	}
	defer d.Close()

	log.Println("listening")
	if err := daemon.ServeIPC(d); err != nil {
		log.Fatal(err)
	}
}
