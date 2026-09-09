package digest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"time"
)

type SMTPConfig struct {
	Addr     string
	From     string
	Username string
	Password string
	StartTLS bool
	Timeout  time.Duration
}

type SMTPSender struct {
	config SMTPConfig
	now    func() time.Time
}

func NewSMTPSender(config SMTPConfig, now func() time.Time) (*SMTPSender, error) {
	if now == nil || config.Timeout <= 0 {
		return nil, errors.New("invalid SMTP sender configuration")
	}
	if _, _, err := net.SplitHostPort(config.Addr); err != nil {
		return nil, errors.New("invalid SMTP address")
	}
	if _, err := mail.ParseAddress(config.From); err != nil {
		return nil, errors.New("invalid SMTP sender address")
	}
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("invalid SMTP credentials")
	}
	return &SMTPSender{config: config, now: now}, nil
}

func (sender *SMTPSender) Send(ctx context.Context, message Message) error {
	raw, envelopeFrom, err := BuildRFCMessage(sender.config.From, message, sender.now())
	if err != nil {
		return err
	}
	recipient, err := mail.ParseAddress(message.To)
	if err != nil {
		return errors.New("invalid SMTP recipient")
	}
	host, _, _ := net.SplitHostPort(sender.config.Addr)
	dialer := net.Dialer{Timeout: sender.config.Timeout}
	connection, err := dialer.DialContext(ctx, "tcp", sender.config.Addr)
	if err != nil {
		return fmt.Errorf("dial SMTP: %w", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(sender.config.Timeout)); err != nil {
		return fmt.Errorf("set SMTP deadline: %w", err)
	}
	cancelWatch := make(chan struct{})
	defer close(cancelWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-cancelWatch:
		}
	}()

	client, err := smtp.NewClient(connection, host)
	if err != nil {
		return fmt.Errorf("open SMTP client: %w", err)
	}
	defer client.Close()
	if sender.config.StartTLS {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	if sender.config.Username != "" {
		auth := smtp.PlainAuth("", sender.config.Username, sender.config.Password, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authenticate SMTP: %w", err)
		}
	}
	if err := client.Mail(envelopeFrom); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(recipient.Address); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	data, err := client.Data()
	if err != nil {
		return fmt.Errorf("open SMTP data: %w", err)
	}
	if _, err := data.Write(raw); err != nil {
		_ = data.Close()
		return fmt.Errorf("write SMTP data: %w", err)
	}
	if err := data.Close(); err != nil {
		return fmt.Errorf("finish SMTP data: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("quit SMTP: %w", err)
	}
	return nil
}
