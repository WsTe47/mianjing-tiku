// Command server 启动 nc-interview 的 HTTP 服务（API + 静态前端）。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/climber47/nc-interview/internal/api"
	"github.com/climber47/nc-interview/internal/config"
	"github.com/climber47/nc-interview/internal/store"
)

func main() {
	cfg := config.Load()

	st, err := store.Open(cfg.DSN)
	if err != nil {
		log.Fatalf("连接数据库失败: %v\n提示：先执行 `mysql -u root -p < scripts/setup.sql` 建库建用户，并在 .env 里填好密码。", err)
	}
	defer st.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(st, cfg.WebDir, cfg.CORSAllow).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("nc-interview 已启动 → http://%s", cfg.Addr)
		log.Printf("  数据库: %s", cfg.RedactedDSN())
		log.Printf("  前端:   %s", cfg.WebDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("服务异常退出: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("关闭超时: %v", err)
	}
	log.Println("已停止")
}
