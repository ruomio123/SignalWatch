package logging

import (
	"fmt"
	"io"
	"log/slog"
)

// 统一管理、方便排查问题和接入日志系统。
func New(out io.Writer, service string, levelText string) (*slog.Logger, error) {
	// 解析配置中的最低日志级别，非法级别会阻止 Logger 创建。
	var level slog.Level
	if err := level.UnmarshalText([]byte(levelText)); err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", levelText, err)
	}

	// 创建 JSON Handler，并为每条日志绑定公共的服务名称字段。
	options := &slog.HandlerOptions{Level: level}        //告诉日志系统过滤规则
	handler := slog.NewJSONHandler(out, options)         //决定日志怎么格式化、写到哪里
	logger := slog.New(handler).With("service", service) //生成业务使用的 Logger并给所有日志添加公共信息

	return logger, nil
}
