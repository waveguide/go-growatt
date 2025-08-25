package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	MQTT "github.com/eclipse/paho.mqtt.golang"
)

type SlogLogger struct {
	Level slog.Level
}

func (l SlogLogger) Println(v ...interface{}) {
	msg := fmt.Sprint(v...)
	slog.Log(context.Background(), l.Level, msg)
}

func (l SlogLogger) Printf(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	slog.Log(context.Background(), l.Level, msg)
}

type Mqtt struct {
	client    MQTT.Client
	User      string
	Password  string
	BrokerURI string
	ClientID  string
}

func (m *Mqtt) Init() error {
	// Set Paho MQTT's internal logger to use slog
	MQTT.CRITICAL = SlogLogger{Level: slog.LevelError}
	MQTT.ERROR = SlogLogger{Level: slog.LevelError}
	MQTT.WARN = SlogLogger{Level: slog.LevelWarn}
	// MQTT.DEBUG = SlogLogger{Level: slog.LevelDebug}

	opts := MQTT.NewClientOptions()
	opts.AddBroker(m.BrokerURI)
	opts.Username = m.User
	opts.Password = m.Password
	opts.SetClientID(m.ClientID)
	opts.SetKeepAlive(60 * time.Second)

	// Create and connect the client
	m.client = MQTT.NewClient(opts)
	if token := m.client.Connect(); token.Wait() && token.Error() != nil {
		slog.Error(token.Error().Error())
		return token.Error()
	}

	return nil
}

func (m *Mqtt) Publish(topic string, payload string, retain bool) {
	token := m.client.Publish(topic, 0, retain, payload)
	token.Wait()

	slog.Debug(fmt.Sprintf("Published message to topic %q: %s", topic, payload))
}

func (m *Mqtt) Disconnect() {
	m.client.Disconnect(250)
}
