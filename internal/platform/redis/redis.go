package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"signalwatch/internal/platform/config"
)

const pingTimeout = 5 * time.Second

/*
Open 根据应用配置创建并验证 Redis Client。
参数：
  - cfg：已经由 config.Load() 读取和校验过的完整配置。

返回值：
  - PING 成功时，返回可以继续使用的 Redis Client。
  - 创建连接或 PING 失败时，返回 nil 和错误。

注意：
  - 本函数不读取环境变量，所有配置都从 Config 获取。
  - NewClient 本身通常不会立即确认 Redis 是否可用，
    因此后面必须主动执行一次 PING。
*/
func Open(cfg config.Config) (*goredis.Client, error) {
	// NewClient 只创建客户端对象，真正的连接可用性需要通过后面的 PING 验证。
	client := goredis.NewClient(&goredis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	err := client.Ping(ctx).Err()
	if err != nil {
		closeErr := client.Close()
		if closeErr != nil {
			return nil, fmt.Errorf(
				"ping redis: %w; close redis client: %v",
				err,
				closeErr,
			)
		}
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}
