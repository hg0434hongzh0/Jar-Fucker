package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"

	"github.com/hg0434hongzh0/Jar-Fucker/internal/handler"
)

//go:embed web/*
var webFS embed.FS

func main() {
	port := "9527"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	webContent, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("无法加载 web 资源: %v", err)
	}

	h := handler.New(webContent)

	addr := "127.0.0.1:" + port
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatalf("无法启动服务: %v", err)
		}
	}

	url := "http://" + listener.Addr().String()

	fmt.Println()
	fmt.Println("  ╔═══════════════════════════════════════════╗")
	fmt.Println("  ║          ☕ Jar-Fucker v1.0.0             ║")
	fmt.Println("  ║      JAR 包提取 & CFR 反编译工具          ║")
	fmt.Println("  ╠═══════════════════════════════════════════╣")
	fmt.Printf("  ║  🌐 %-38s ║\n", url)
	fmt.Println("  ║  按 Ctrl+C 退出                           ║")
	fmt.Println("  ╚═══════════════════════════════════════════╝")
	fmt.Println()

	openBrowser(url)

	log.Fatal(http.Serve(listener, h))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
