package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func waitForSignals(srv *http.Server) error {
	sig := make(chan os.Signal, 1024)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	for {
		select {
		case s := <-sig:
			switch s {
			case syscall.SIGHUP:
				err := handleHangup() // 关闭
				if err == nil {
					// no error occured - child spawned and started
					return shutdown(srv)
				}
			case syscall.SIGTERM, syscall.SIGINT:
				return shutdown(srv)
			}
		}
	}
}

func handleHangup() error {
	c := make(chan string)
	defer close(c)
	errChn := make(chan error)
	defer close(errChn)

	go socketListener(c, errChn)

	for {
		select {
		case cmd := <-c:
			switch cmd {
			case "socket_opened":
				p, err := fork()
				if err != nil {
					fmt.Printf("unable to fork: %v\n", err)
					continue
				}
				fmt.Printf("forked (PID: %d), waiting for spinup", p.Pid)

			case "listener_sent":
				fmt.Println("listener sent - shutting down")

				return nil
			}

		case err := <-errChn:
			return err
		}
	}

	return nil
}

func socketListener(chn chan<- string, errChn chan<- error) {
	// 创建 socket 服务端
	fmt.Println("创建新的 socket listener...")
	ln, err := net.Listen("unix", cfg.sockFile)
	if err != nil {
		fmt.Println("socketListener error", err.Error())
		errChn <- err
		return
	}
	defer ln.Close()

	// signal that we created a socket
	chn <- "socket_opened" // 这时候已经开始 fork 了

	// accept
	c, err := acceptConn(ln)
	if err != nil {
		errChn <- err
		return
	}

	// read from the socket
	fmt.Println("服务端读取socket")
	buf := make([]byte, 512)
	nr, err := c.Read(buf)
	if err != nil {
		errChn <- err
		return
	}

	data := buf[0:nr]
	fmt.Println("获得消息", string(data))
	switch string(data) {
	case "get_listener":
		fmt.Println("get_listener received - sending listener information")
		err := sendListener(c) // 发送文件描述到新的子进程，用来 import Listener
		if err != nil {
			errChn <- err
			return
		}
		chn <- "listener_sent"
	}
}


func acceptConn(l net.Listener) (c net.Conn, err error) {
	chn := make(chan error)
	go func() {
		defer close(chn)
		fmt.Printf("accept 新连接%+v\n", l)
		c, err = l.Accept()
		if err != nil {
			chn <- err
		}
	}()

	select {
	case err = <-chn:
		if err != nil {
			fmt.Printf("error occurred when accepting socket connection: %v\n",
				err)
		}

	case <-time.After(cfg.childTimeout):
		fmt.Println("timeout occurred waiting for connection from child")
	}

	return
}


func sendListener(c net.Conn) error {
	fmt.Printf("发送老的 listener 文件 %+v\n", cfg.ln)
	lnFile, err := getListenerFile(cfg.ln)
	if err != nil {
		return err
	}
	defer lnFile.Close()

	l := listener{
		Addr:     cfg.addr,
		FD:       3, // 0 stdin 1 stdout 2 stderr
		Filename: lnFile.Name(),
	}

	lnEnv, err := json.Marshal(l)
	if err != nil {
		return err
	}
	fmt.Printf("将 %+v\n 写入连接\n", string(lnEnv))
	_, err = c.Write(lnEnv)
	if err != nil {
		return err
	}

	return nil
}

