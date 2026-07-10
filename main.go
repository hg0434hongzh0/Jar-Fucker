package main

import (
	"context"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/hg0434hongzh0/Jar-Fucker/internal/handler"
)

//go:embed web
var webFS embed.FS

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	port := "9527"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	webContent, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("无法加载 web 资源: %v", err)
	}

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatalf("无法启动服务: %v", err)
		}
	}

	sessionToken := strings.TrimSpace(os.Getenv("JAR_FUCKER_SESSION_TOKEN"))
	if sessionToken == "" {
		sessionToken, err = handler.NewSessionToken()
		if err != nil {
			log.Fatal(err)
		}
	} else if raw, decodeErr := base64.RawURLEncoding.DecodeString(sessionToken); decodeErr != nil || len(raw) != 32 {
		log.Fatal("JAR_FUCKER_SESSION_TOKEN 必须是 256-bit base64url 令牌")
	}
	h := handler.New(webContent, handler.Options{SessionToken: sessionToken})
	baseURL := "http://" + listener.Addr().String()
	launchURL := baseURL + "/#token=" + url.QueryEscape(sessionToken)

	fmt.Println()
	fmt.Printf("  Jar-Fucker %s (%s, %s)\n", version, commit, buildDate)
	fmt.Println("  JAR analysis, extraction and Fernflower decompilation")
	fmt.Printf("  %s\n", launchURL)
	fmt.Println("  Press Ctrl+C to stop")
	fmt.Println()

	if os.Getenv("NO_BROWSER") == "" {
		openBrowser(launchURL)
	}

	server := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("服务异常退出: %v", err)
	}
}

func openBrowser(targetURL string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", targetURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
	default:
		cmd = exec.Command("xdg-open", targetURL)
	}
	_ = cmd.Start()
}
