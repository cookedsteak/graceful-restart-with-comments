# graceful-restart-with-comments
https://tomaz.lovrec.eu/posts/graceful-server-restart  中文注解

本文参考 [GRACEFULLY RESTARTING A GOLANG WEB SERVER](https://tomaz.lovrec.eu/posts/graceful-server-restart/)
进行归纳和说明。

你也可以从[这里](https://github.com/cookedsteak/graceful-restart-with-comments)拿到添加备注的代码版本。
我做了下分割，方便你能看懂。

## 问题
因为 golang 是编译型的，所以当我们修改一个用 go 写的服务的配置后，需要重启该服务，有的甚至还需要重新编译，再发布。如果在重启的过程中有大量的请求涌入，能做的无非是分流，或者堵塞请求。不论哪一种，都不优雅~，所以slax0r以及他的团队，就试图探寻一种更加平滑的，便捷的重启方式。

原文章中除了排版比较帅外，文字内容和说明还是比较少的，所以我希望自己补充一些说明。

## 原理
上述问题的根源在于，我们无法同时让两个服务，监听同一个端口。
解决方案就是复制当前的 listen 文件，然后在新老进程之间通过 socket 直接传输参数和环境变量。
新的开启，老的关掉，就这么简单。

#### 防看不懂须知
[Unix domain socket](https://en.wikipedia.org/wiki/Unix_domain_socket)

[一切皆文件](https://www.zhihu.com/question/25696682)

## 先玩一下
运行程序，过程中打开一个新的 console，输入 `kill -1 [进程号]`，你就能看到优雅重启的进程了。

## 代码思路
因为 http server 的运行需要一个监听对象，该对象包含了我们需要监听的
```
func main() {
    主函数，初始化配置
    调用serve()
}

func serve() {
    核心运行函数
    getListener()   // 1. 获取监听 listener
    start()         // 2. 用获取到的 listener 开启 server 服务
    waitForSignal() // 3. 监听外部信号，用来控制程序 fork 还是 shutdown
}

func getListener() {
    获取正在监听的端口对象
    （第一次运行新建）
}

func start() {
    运行 http server
}

func waitForSignal() {
    for {
        等待外部信号
        1. fork子进程
        2. 关闭进程
    }
}
```
上面是代码思路的说明，基本上我们就围绕这个大纲填充完善代码。

## 定义结构体
我们抽象出两个结构体，描述程序中公用的数据结构
```
var cfg *srvCfg
type listener struct {
	// Listener address
	Addr string `json:"addr"`
	// Listener file descriptor
	FD int `json:"fd"`
	// Listener file name
	Filename string `json:"filename"`
}

type srvCfg struct {
	sockFile string
	addr string
	ln net.Listener
	shutDownTimeout time.Duration
	childTimeout time.Duration
}
```
listener 是我们的监听者，他包含了监听地址，文件描述符，文件名。
文件描述符其实就是进程所需要打开的文件的一个索引，非负整数。
我们平时创建一个进程时候，linux都会默认打开三个文件，标准输入stdin,标准输出stdout,标准错误stderr，
这三个文件各自占用了 0，1，2 三个文件描述符。所以之后你进程还要打开文件的话，就得从 3 开始了。
这个listener，就是我们进程之间所要传输的数据了。

srvCfg 是我们的全局环境配置，包含 socket file 路径，服务监听地址，监听者对象，父进程超时时间，子进程超时时间。
因为是全局用的配置数据，我们先 var 一下。

## 入口
看看我们的 main 长什么样子
```
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
	cfg = &config
	var err error
	// get tcp listener
	cfg.ln, err = getListener()
	if err != nil {
		panic(err)
	}

	// return an http Server
	srv := start(handler)

	// create a wait routine
	err = waitForSignals(srv)
	if err != nil {
		panic(err)
	}
}
```
很简单，我们把配置都准备好了，然后还注册了一个 handler--输出 Hello, world!

serve 函数的内容就和我们之前的思路一样，只不过多了些错误判断。

接下去，我们一个一个看里面的函数...

## 获取 listener
也就是我们的 getListener() 函数
```
func getListener() (net.Listener, error) {
    // 第一次执行不会 importListener
	ln, err := importListener()
	if err == nil {
		fmt.Printf("imported listener file descriptor for addr: %s\n", cfg.addr)
		return ln, nil
	}
    // 第一次执行会 createListener
	ln, err = createListener()
	if err != nil {
		return nil, err
	}

	return ln, err
}

func importListener() (net.Listener, error) {
    ...
}

func createListener() (net.Listener, error) {
	fmt.Println("首次创建 listener", cfg.addr)
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return nil, err
	}

	return ln, err
}
```
因为第一次不会执行 importListener， 所以我们暂时不需要知道 importListener 里是怎么实现的。
只肖明白 createListener 返回了一个监听对象。

而后就是我们的 start 函数
```
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
```
很明显，start 传入一个 handler，然后协程运行一个 http server。

## 监听信号
监听信号应该是我们这篇里面重头戏的入口，我们首先来看下代码：
```
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
```
首先建立了一个通道，这个通道用来接收系统发送到程序的命令，比如`kill -9 myprog`，
这个 `9` 就是传到通道里的。我们用 Notify 来限制会产生响应的信号，这里有：
- SIGTERM
- SIGINT
- SIGHUP
[关于信号](https://unix.stackexchange.com/questions/251195/difference-between-less-violent-kill-signal-hup-1-int-2-and-term-15)

如果实在搞不清这三个信号的区别，只要明白我们通过区分信号，留给了进程自己判断处理的余地。

然后我们开启了一个循环监听，显而易见地，监听的就是系统信号。
当信号为 `syscall.SIGHUP` ，我们就要重启进程了。
而当信号为 `syscall.SIGTERM, syscall.SIGINT` 时，我们直接关闭进程。

于是乎，我们就要看看，`handleHangup` 里面到底做了什么。

## 父子间的对话
进程之间的优雅重启，我们可以看做是一次愉快的父子对话，
爸爸给儿子开通了一个热线，爸爸通过热线把现在正在监听的端口信息告诉儿子，
儿子在接受到必要的信息后，子承父业，开启新的空进程，告知爸爸，爸爸正式退休。
```
func handleHangup() error {
	c := make(chan string)
	defer close(c)
	errChn := make(chan error)
	defer close(errChn)
    // 开启一个热线通道
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
```
socketListener 开启了一个新的 unix socket 通道，同时监听通道的情况，并做相应的处理。
处理的情况说白了就只有两种：
1. 通道开了，说明我可以造儿子了（fork），儿子来接爸爸的信息
2. 爸爸把监听对象文件都传给儿子了，爸爸完成使命

`handleHangup` 里面的东西有点多，不要慌，我们一个一个来看。
先来看 `socketListener`：
```
func socketListener(chn chan<- string, errChn chan<- error) {
	// 创建 socket 服务端
	fmt.Println("创建新的socket通道")
	ln, err := net.Listen("unix", cfg.sockFile)
	if err != nil {
		errChn <- err
		return
	}
	defer ln.Close()

	// signal that we created a socket
	fmt.Println("通道已经打开，可以 fork 了")
	chn <- "socket_opened"

	// accept
	// 阻塞等待子进程连接进来
	c, err := acceptConn(ln)
	if err != nil {
		errChn <- err
		return
	}

	// read from the socket
	buf := make([]byte, 512)
	nr, err := c.Read(buf)
	if err != nil {
		errChn <- err
		return
	}

	data := buf[0:nr]
	fmt.Println("获得消息子进程消息", string(data))
	switch string(data) {
	case "get_listener":
		fmt.Println("子进程请求 listener 信息，开始传送给他吧~")
		err := sendListener(c) // 发送文件描述到新的子进程，用来 import Listener
		if err != nil {
			errChn <- err
			return
		}
		// 传送完毕
		fmt.Println("listener 信息传送完毕")
		chn <- "listener_sent"
	}
}
```
`sockectListener`创建了一个 unix socket 通道，创建完毕后先发送了 `socket_opened` 这个信息。
这时候 `handleHangup` 里的 `case "socket_opened"` 就会有反应了。
同时，`socketListener` 一直在 accept 阻塞等待新程序的信号，从而发送原 `listener` 的文件信息。
直到发送完毕，才会再告知 `handlerHangup` `listener_sent`。

下面是 acceptConn 的代码，并没有复杂的逻辑，就是等待子程序请求、处理超时和错误。
```
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
```

还记的我们之前定义的 listener 结构体吗？这时候就要派上用场了：
```
func sendListener(c net.Conn) error {
	fmt.Printf("发送老的 listener 文件 %+v\n", cfg.ln)
	lnFile, err := getListenerFile(cfg.ln)
	if err != nil {
		return err
	}
	defer lnFile.Close()

	l := listener{
		Addr:     cfg.addr,
		FD:       3, // 文件描述符，进程初始化描述符为0 stdin 1 stdout 2 stderr，所以我们从3开始
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

func getListenerFile(ln net.Listener) (*os.File, error) {
	switch t := ln.(type) {
	case *net.TCPListener:
		return t.File()
	case *net.UnixListener:
		return t.File()
	}

	return nil, fmt.Errorf("unsupported listener: %T", ln)
}
```
`sendListener` 先将我们正在使用的tcp监听文件（一切皆文件）做了一份拷贝，并把必要的信息塞进了
`listener` 结构体中，序列化后用 unix socket 传输给新的子进程。

说了这么多都是爸爸进程的代码，中间我们跳过了创建子进程，
那下面我们来看看 `fork`，也是一个重头戏：
```
func fork() (*os.Process, error) {
	// 拿到原监听文件描述符并打包到元数据中
	lnFile, err := getListenerFile(cfg.ln)
	fmt.Printf("拿到监听文件 %+v\n，开始创建新进程\n", lnFile.Name())
	if err != nil {
		return nil, err
	}
	defer lnFile.Close()

	// 创建子进程时必须要塞的几个文件
	files := []*os.File{
		os.Stdin,
		os.Stdout,
		os.Stderr,
		lnFile,
	}

	// 拿到新进程的程序名，因为我们是重启，所以就是当前运行的程序名字
	execName, err := os.Executable()
	if err != nil {
		return nil, err
	}
	execDir := filepath.Dir(execName)

	// 生孩子了
	p, err := os.StartProcess(execName, []string{execName}, &os.ProcAttr{
		Dir:   execDir,
		Files: files,
		Sys:   &syscall.SysProcAttr{},
	})
	fmt.Println("创建子进程成功")
	if err != nil {
		return nil, err
	}
	// 这里返回 nil 后就会直接 shutdown 爸爸进程
	return p, nil
}
```
当执行 `StartProcess` 的那一刻，你会意识到，子进程的执行会回到最初的地方，也就是 main 开始。
这时候，我们 [获取 listener](##获取-listener)中的 `importListener` 方法就会被激活：
```
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
```
这里的 importListener 执行时间，就是在父进程创建完新的 unix socket 通道后。

`至此，子进程开始了新的一轮监听，服务...`

## 结束
代码量虽然不大，但是传递了一个很好的优雅重启思路，有些地方还是要实践一下才能理解（对于我这种新手而言）。
其实网上还有很多其他优雅重启的方式，大家可以 Google 一下。
希望我上面简单的讲解能够帮到你，如果有错误的话请及时指出，我会更正的。

你也可以从[这里](https://github.com/cookedsteak/graceful-restart-with-comments)拿到添加备注的代码版本。
我做了下分割，方便你能看懂。