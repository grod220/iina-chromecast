package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	parentPID := flag.Int("parent", os.Getppid(), "owning IINA process")
	control := flag.String("control", "127.0.0.1:9092", "localhost control address")
	media := flag.String("media", "0.0.0.0:8010", "local media address")
	ip := flag.String("ip", "", "LAN address advertised to TV")
	logPath := flag.String("log", defaultLogPath(), "log file")
	level := flag.Int("v", lvlInfo, "logging verbosity")
	flag.Parse()
	setupLogging(*logPath, *level)
	_, port, err := net.SplitHostPort(*media)
	if err != nil {
		panic(err)
	}
	exe, _ := os.Executable()
	a := newApp("http://"+net.JoinHostPort(*ip, port), filepath.Dir(exe))
	a.parentPID = *parentPID
	a.autoAddress = *ip == ""
	a.mediaPort = port
	ctrlMux, mediaMux := a.handlers()
	ml, err := net.Listen("tcp", *media)
	if err != nil {
		logError("Media listener: %v", err)
		os.Exit(1)
	}
	cl, err := net.Listen("tcp", *control)
	if err != nil {
		ml.Close()
		logError("Control listener: %v", err)
		os.Exit(1)
	}
	go http.Serve(ml, mediaMux)
	go http.Serve(cl, a.controlHandler(ctrlMux))
	go a.monitor(context.Background())
	parent := *parentPID
	if parent > 1 {
		go func() {
			for {
				time.Sleep(3 * time.Second)
				if syscall.Kill(parent, 0) != nil {
					a.stopForExit(2 * time.Second)
					os.Exit(0)
				}
			}
		}()
	}
	fmt.Println("READY")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	<-signals
	a.stopForExit(2 * time.Second)
}
