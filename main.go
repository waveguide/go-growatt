package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/waveguide/go-growatt/inverter"
	"github.com/waveguide/go-growatt/logger"
	"github.com/waveguide/go-growatt/mqtt"
	"gopkg.in/yaml.v3"
)

// config struct is filled when the yaml config file is read upon startup.
type config struct {
	LogLevel string `yaml:"log_level"`
	Inverter struct {
		Address  string `yaml:"address"`
		BaudRate int    `yaml:"baudrate"`
	} `yaml:"inverter"`
	Mqtt struct {
		User      string `yaml:"user"`
		Password  string `yaml:"password"`
		ClientID  string `yaml:"client_id"`
		BrokerURI string `yaml:"broker_uri"`
		Topic     string `yaml:"topic"`
	} `yaml:"mqtt"`
}

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s <config-file.yaml>", os.Args[0])
	}

	cfg, err := readConfig(os.Args[1])
	if err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	if err := logger.Setup(cfg.LogLevel); err != nil {
		log.Fatal(err)
	}
	slog.Info("Starting")

	stopSigs := make(chan os.Signal, 1)
	signal.Notify(stopSigs, os.Interrupt, syscall.SIGTERM)

	// Connect to MQTT broker
	mqttClient := mqtt.Mqtt{
		User:      cfg.Mqtt.User,
		Password:  cfg.Mqtt.Password,
		BrokerURI: cfg.Mqtt.BrokerURI,
		ClientID:  cfg.Mqtt.ClientID,
	}

	if err := mqttClient.Init(); err != nil {
		log.Fatal("Failed to connect to MQTT broker")
	}
	defer mqttClient.Disconnect()

	// Channel carrying power-rate commands from MQTT into the reader goroutine.
	// Buffered with 'latest wins' semantics so a slow reader never blocks paho's
	// callback and only the most recent command is applied.
	// Example publishing a message: mosquitto_pub -t 'solar/powerrate/set' -m '50'
	powerRateChan := make(chan uint16, 1)

	cmdTopic := fmt.Sprintf("%s/powerrate/set", cfg.Mqtt.Topic)
	if err := mqttClient.Subscribe(cmdTopic, func(payload string) {
		rate, err := strconv.Atoi(strings.TrimSpace(payload))
		if err != nil || rate < 0 || rate > 100 {
			slog.Warn(fmt.Sprintf("Ignoring invalid power rate command %q on %s", payload, cmdTopic))
			return
		}

		// Latest wins: discard any stale pending command, then enqueue this one.
		select {
		case <-powerRateChan: // if a value is buffered, remove it
		default:
		}
		powerRateChan <- uint16(rate)
	}); err != nil {
		log.Fatal("Failed to subscribe to power rate command topic")
	}

	// Connect to inverter
	inv := inverter.Inverter{
		Address:  cfg.Inverter.Address,
		BaudRate: cfg.Inverter.BaudRate,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// inv.Run owns the inverter connection: it connects on start and
	// disconnects when it returns. Cancel the context and wait for that
	// goroutine to fully stop before main returns.
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	dataChan := make(chan inverter.Stats)
	wg.Add(1)
	go func() {
		defer wg.Done()
		inv.Run(ctx, dataChan, powerRateChan)
	}()

	lastInverterState := inverter.InverterStateCodes[0]
	for {
		select {
		case msg := <-dataChan:
			slog.Debug(fmt.Sprintf("Received inverter stats: %v", msg))
			if lastInverterState != msg.State {
				// Inverter state is changed. Write retained mqtt message to state topic.
				jsonBytes, err := json.Marshal(
					inverter.InverterState{
						State:     msg.State,
						Timestamp: time.Now().UTC().Format("2006-01-02 15:04:05"),
					},
				)
				if err != nil {
					slog.Error(
						fmt.Sprintf("Failed to convert InverterState struct to json: %v",
							err,
						),
					)
					return // Quit
				}
				slog.Info(
					fmt.Sprintf(
						"Change inverter state from %q to %q",
						lastInverterState,
						msg.State,
					),
				)
				mqttClient.Publish(
					fmt.Sprintf("%s/state", cfg.Mqtt.Topic),
					string(jsonBytes),
					true,
				)
				lastInverterState = msg.State
			}

			// Publish stats to mqtt
			jsonBytes, err := json.Marshal(msg)
			if err != nil {
				slog.Error(
					fmt.Sprintf("Failed to convert inverter.Stats struct to json: %v",
						err,
					),
				)
				return // Quit
			}
			mqttClient.Publish(
				fmt.Sprintf("%s/data", cfg.Mqtt.Topic),
				string(jsonBytes),
				true,
			)

		case sig := <-stopSigs:
			slog.Info(fmt.Sprintf("Stopping! Got signal %v", sig))
			return
		}
	}
}

func readConfig(configPath string) (config, error) {
	var cfg config

	data, err := os.ReadFile(configPath)
	if err != nil {
		return config{}, err
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("error parsing YAML: %w", err)
	}

	// TODO: Validate more variables

	if cfg.Mqtt.Topic == "" {
		return cfg, fmt.Errorf("mqtt.topic is empty or absent")
	}

	return cfg, nil
}
