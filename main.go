package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
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

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

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

	// Connect to inverter
	inv := inverter.Inverter{
		Address:  cfg.Inverter.Address,
		BaudRate: cfg.Inverter.BaudRate,
	}
	if err := inv.Connect(); err != nil {
		log.Fatal("Failed to connect to inverter")
	}
	defer inv.Disconnect()

	// Upon start always check (and set when needed) time on inverter
	if err := inv.CheckSetTime(); err != nil {
		// Log error and continue
		slog.Warn(fmt.Sprintf("Failed to CheckSetTime on inverter upon start: %v", err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dataChan := make(chan inverter.Stats)
	go inv.Read(ctx, dataChan)

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

		case sig := <-sigs:
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
		log.Fatalf("Error parsing YAML: %v", err)
	}

	// TODO: Validate more variables

	if cfg.Mqtt.Topic == "" {
		return cfg, fmt.Errorf("mqtt.topic is empty or absent")
	}

	return cfg, nil
}
