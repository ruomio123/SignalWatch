package main

import (
	"log/slog"
	"net/http"
)

func main() {
	address := "127.0.0.1:8080"
	mux := http.NewServeMux() //创建独立的 ServeMux

	//注册带请求方法的路由
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {

		//构造 JSON 响应
		w.Header().Set("Content-Type", "application/json")                       //1.设置响应头
		w.WriteHeader(200)                                                       //2.设置状态码
		_, err := w.Write([]byte(`{"status":"ok","service":"signalwatch-api"}`)) //3.写入响应正文
		if err != nil {
			slog.Error("API server stopped", "error", err)
			return
		}
	})

	//创建自己的 HTTP Server
	server := &http.Server{
		Addr:    address,
		Handler: mux,
	}
	//启动服务；出现错误时记录日志并退出
	slog.Info("my first API server starting", "address is:", address)
	err := server.ListenAndServe()
	if err != nil {
		slog.Error("API server stopped", "error", err)
		return
	}
}
