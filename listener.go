package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"io"
)

func getListener() (net.Listener, error) {
	// 第二次执行，将原来的 listener 拿过来
	ln, err := importListener()
	if err == nil {
		fmt.Printf("imported listener file descriptor for addr: %s\n", cfg.addr)
		return ln, nil
	}

	ln, err = createListener()
	if err != nil {
		return nil, err
	}

	return ln, err
}


func importListener() (net.Listener, error) {
	// 向已经准备好的 unix socket 建立连接，这个是爸爸进程在之前就建立好的
	c, err := net.Dial("unix", cfg.sockFile)
	if err != nil {
		fmt.Println("no unix socket now")
		return nil, err
	}
	defer c.Close()
	fmt.Println("准备导入原 listener 文件...")
	var lnEnv string
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func(r io.Reader) {
		defer wg.Done()
		// 读取 conn 中的内容
		buf := make([]byte, 1024)
		n, err := r.Read(buf[:])
		if err != nil {
			return
		}

		lnEnv = string(buf[0:n])
	}(c)
	// 写入 get_listener
	fmt.Println("告诉爸爸我要 'get-listener' 了")
	_, err = c.Write([]byte("get_listener"))
	if err != nil {
		return nil, err
	}

	wg.Wait() // 等待爸爸传给我们参数

	if lnEnv == "" {
		return nil, fmt.Errorf("Listener info not received from socket")
	}

	var l listener
	err = json.Unmarshal([]byte(lnEnv), &l)
	if err != nil {
		return nil, err
	}
	if l.Addr != cfg.addr {
		return nil, fmt.Errorf("unable to find listener for %v", cfg.addr)
	}

	// the file has already been passed to this process, extract the file
	// descriptor and name from the metadata to rebuild/find the *os.File for
	// the listener.
	// 我们已经拿到了监听文件的信息，我们准备自己创建一份新的文件并使用
	lnFile := os.NewFile(uintptr(l.FD), l.Filename)
	fmt.Println("新文件名：", l.Filename)
	if lnFile == nil {
		return nil, fmt.Errorf("unable to create listener file: %v", l.Filename)
	}
	defer lnFile.Close()

	// create a listerer with the *os.File
	ln, err := net.FileListener(lnFile)
	if err != nil {
		return nil, err
	}

	return ln, nil
}

func createListener() (net.Listener, error) {
	fmt.Println("首次创建 listener", cfg.addr)
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return nil, err
	}

	return ln, err
}

// obtain the *os.File from the listener
// 返回 tcp 监听副本
func getListenerFile(ln net.Listener) (*os.File, error) {
	switch t := ln.(type) {
	case *net.TCPListener:
		return t.File()
	case *net.UnixListener:
		return t.File()
	}

	return nil, fmt.Errorf("unsupported listener: %T", ln)
}