package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var cfg *srvCfg

type listener struct {
	// 监听地址
	Addr string `json:"addr"`
	// 监听文件描述符
	FD int `json:"fd"`
	// 监听文件名字
	Filename string `json:"filename"`
}


type srvCfg struct {
	sockFile string
	addr string
	ln net.Listener
	shutDownTimeout time.Duration
	childTimeout time.Duration
}

func main() {
	serve(srvCfg{
		sockFile: "/tmp/api.sock",
		addr:     ":8000",
		shutDownTimeout: 5*time.Second,
		childTimeout: 5*time.Second,
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`Hello, world!`))
	}))
}

func serve(config srvCfg, handler http.Handler) {
	fmt.Println("当前进程 ID 为：", os.Getpid())
	cfg = &config
	var err error
	// 获得监听对象
	cfg.ln, err = getListener()
	if err != nil {
		panic(err)
	}

	// 启动 http server
	srv := start(handler)

	// 创建信号监听协程
	err = waitForSignals(srv)
	if err != nil {
		panic(err)
	}
}

func start(handler http.Handler) *http.Server {
	srv := &http.Server{
		Addr: cfg.addr,
		Handler: handler,
	}
	// start to serve
	go srv.Serve(cfg.ln)
	fmt.Println("server 启动完成，配置信息为：",cfg.ln)
	return srv
}


func shutdown(srv *http.Server) error {
	fmt.Println("Server shutting down")
	ctx, cancel := context.WithTimeout(context.Background(),
		cfg.shutDownTimeout)
	defer cancel()

	return srv.Shutdown(ctx)
}

func fork() (*os.Process, error) {
	// 拿到原监听文件描述符并打包到元数据中
	lnFile, err := getListenerFile(cfg.ln)
	fmt.Printf("拿到监听文件 %+v\n，开始创建新进程\n", lnFile.Name())
	if err != nil {
		return nil, err
	}
	defer lnFile.Close()

	// pass the stdin, stdout, stderr, and the listener files to the child
	files := []*os.File{
		os.Stdin,
		os.Stdout,
		os.Stderr,
		lnFile,
	}

	// get process name and dir
	execName, err := os.Executable()
	if err != nil {
		return nil, err
	}
	execDir := filepath.Dir(execName)

	// spawn a child
	p, err := os.StartProcess(execName, []string{execName}, &os.ProcAttr{
		Dir:   execDir,
		Files: files,
		Sys:   &syscall.SysProcAttr{},
	})
	fmt.Println("创建子进程成功")
	if err != nil {
		return nil, err
	}

	return p, nil
}
