package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"signalwatch/internal/platform/config"
)

const pingTimeout = 5 * time.Second

/*
功能：根据应用配置创建、配置并验证 MySQL 数据库连接。
参数：cfg 是已经由 config.Load() 读取和校验完成的完整配置。
返回值：连接成功时返回可供后续 Repository 使用的 *gorm.DB；

	创建连接、获取连接池或 Ping 数据库失败时返回错误。
*/
func Open(cfg config.Config) (*gorm.DB, error) {
	// mysql.Open 只负责把 DSN 交给 MySQL Driver
	// 此时未必已经真正建立 TCP 连接，因此后面还必须 Ping
	database, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
	if err != nil {
		//不把 cfg.MySQLDSN 拼到错误中，避免密码进入日志
		return nil, fmt.Errorf("open mysql connection: %w", err)
	}
	//GORM 在底层仍然使用 Go 标准库 database/sql 的 *sql.DB
	sqlDB, err := database.DB()
	if err != nil {
		return nil, fmt.Errorf("get mysql connection pool:%w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MySQLMaxOpenConns) // 最大打开连接数：超过这个数量时，新请求会等待可用连接
	sqlDB.SetMaxIdleConns(cfg.MySQLMaxIdleConns) // 最大空闲连接数：请求结束后最多保留多少连接，以便下次复用

	// Ping 可能遇到网络故障、错误密码或 MySQL 未启动
	// 超时 Context 防止程序在启动阶段无限卡住
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	err = sqlDB.PingContext(ctx) //这一步开始尝试与数据库建立连接
	if err != nil {
		closeErr := sqlDB.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("ping mysql failed: %w; close connection pool: %v", err, closeErr)
		}
		return nil, fmt.Errorf("ping mysql:%w", err)
	}
	return database, nil
}
