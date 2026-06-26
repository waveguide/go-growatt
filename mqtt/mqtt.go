package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
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

// subscription records a topic and its handler so subscriptions can be
// re-established after the client reconnects to the broker.
type subscription struct {
	topic   string
	handler func(payload string)
}

type Mqtt struct {
	client    MQTT.Client
	User      string
	Password  string
	BrokerURI string
	ClientID  string

	mu   sync.Mutex
	subs []subscription
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

	// Re-establish subscriptions on every (re)connect. With the default
	// clean session, the broker drops our subscriptions when the connection
	// is lost, so they must be recreated once paho reconnects. OnConnect also
	// fires on the initial connect (when no subscriptions exist yet), so this
	// is a no-op until Subscribe has been called.
	opts.SetAutoReconnect(true)
	opts.OnConnect = func(MQTT.Client) {
		m.resubscribe()
	}

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
	if err := token.Error(); err != nil {
		slog.Error(fmt.Sprintf("Failed to publish to topic %q: %v", topic, err))
	} else {
		slog.Debug(fmt.Sprintf("Published to topic %q: %s", topic, payload))
	}
}

func (m *Mqtt) Subscribe(topic string, handler func(payload string)) error {
	// Remember the subscription so it can be restored after a reconnect.
	m.mu.Lock()
	m.subs = append(m.subs, subscription{topic: topic, handler: handler})
	m.mu.Unlock()

	return m.subscribe(topic, handler)
}

func (m *Mqtt) subscribe(topic string, handler func(payload string)) error {
	token := m.client.Subscribe(topic, 1, func(_ MQTT.Client, msg MQTT.Message) {
		handler(string(msg.Payload()))
	})
	token.Wait()
	if err := token.Error(); err != nil {
		return err
	}

	slog.Info(fmt.Sprintf("Subscribed to topic %q", topic))

	return nil
}

// resubscribe re-establishes all known subscriptions. Called from OnConnect
// after the client (re)connects to the broker.
func (m *Mqtt) resubscribe() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, s := range m.subs {
		if err := m.subscribe(s.topic, s.handler); err != nil {
			slog.Error(fmt.Sprintf("Failed to resubscribe to topic %q: %v", s.topic, err))
		}
	}
}

func (m *Mqtt) Disconnect() {
	m.client.Disconnect(250)
}
