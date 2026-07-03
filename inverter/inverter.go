package inverter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/simonvetter/modbus"
)

var InverterStateCodes = map[uint16]string{
	0: "waiting",
	1: "normal",
	3: "fault",
}

// InverterState is used to write json to MQTT when the inverter state changes.
type InverterState struct {
	State     string `json:"state"`
	Timestamp string `json:"timestamp"` // Time in UTC, format 'yyyy-mm-dd hh:mm:ss'
}

type Stats struct {
	Timestamp         string `json:"timestamp"`           // Time in UTC, format 'yyyy-mm-dd hh:mm:ss'
	State             string `json:"state"`               // Run status of inverter
	PVInputPower      uint32 `json:"PV_input_power"`      // Delivered by panels at DC side in watt
	PV1InputVolt      uint16 `json:"PV1_input_volt"`      // String1: Voltage at DC side
	PV1InputCurrent   uint16 `json:"PV1_input_current"`   // String1: Current at DC side
	PV1InputWatt      uint32 `json:"PV1_input_watt"`      // String1: Watt at DC side
	PV2InputVolt      uint16 `json:"PV2_input_volt"`      // String2: Voltage at DC side
	PV2InputCurrent   uint16 `json:"PV2_input_current"`   // String2: Current at DC side
	PV2InputWatt      uint32 `json:"PV2_input_watt"`      // String2: Watt at DC side
	ACWatt            uint32 `json:"AC_watt"`             // Delivered by inverter to net
	ACFrequency       uint16 `json:"AC_frequency"`        // Grid frequency in Hz
	AC1Volt           uint16 `json:"AC1_volt"`            // Grid voltage phase1
	AC1Current        uint16 `json:"AC1_current"`         // Grid output current phase1
	AC1Watt           uint32 `json:"AC1_watt"`            // Grid output watt phase1
	AC2Volt           uint16 `json:"AC2_volt"`            // Grid voltage phase2 (for 3 phase inverter)
	AC2Current        uint16 `json:"AC2_current"`         // Grid output current phase2 (for 3 phase inverter)
	AC2Watt           uint32 `json:"AC2_watt"`            // Grid output watt phase2 (for 3 phase inverter)
	AC3Volt           uint16 `json:"AC3_volt"`            // Grid voltage phase3 (for 3 phase inverter)
	AC3Current        uint16 `json:"AC3_current"`         // Grid output current phase3 (for 3 phase inverter)
	AC3Watt           uint32 `json:"AC3_watt"`            // Grid output watt phase3 (for 3 phase inverter)
	TotalTodayWatt    uint32 `json:"total_today_watt"`    // Total watt today
	TotalAllTimeWatt  uint32 `json:"total_all_time_watt"` // Total watt all time
	TotalWorkTimeSecs uint32 `json:"total_worktime_secs"` // Total work time seconds
	Temperature       uint16 `json:"inverter_temp"`       // Inverter temperature in Celcius
	FaultCode         uint16 `json:"fault_code"`          // Used when the inverter has a fault
	ActivePowerRate   uint16 `json:"active_power_rate"`   // Configured AC output limit as a percentage (0-100)
}

type Inverter struct {
	Address  string
	BaudRate int
	client   *modbus.ModbusClient

	// activePowerRate caches the last known active power rate (holding
	// register 3) so a transient read failure doesn't drop it from the stats.
	activePowerRate uint16

	// cmdMemoryDisabled is true once we've confirmed holding register 2
	// ("PF CMD memory state") is 0, meaning writes to the power control
	// registers (3, 4, 5, 99) are kept in RAM instead of persisted to the
	// inverter's non-volatile memory on every write.
	cmdMemoryDisabled bool
}

func (i *Inverter) connect() error {
	slog.Info("Connecting to inverter")

	// Create and configure client
	c, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:      i.Address,
		Speed:    uint(i.BaudRate),
		DataBits: 8,
		Parity:   modbus.PARITY_NONE,
		StopBits: 1,
		Timeout:  3 * time.Second,
	})
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to create client for inverter(%s): %v", i.Address, err))
		return err
	}

	// Attempt to connect
	if err := c.Open(); err != nil {
		slog.Error(fmt.Sprintf("Failed to connect to inverter(%s): %v", i.Address, err))
		return err
	}

	i.client = c

	slog.Info("Connected to inverter!")

	return nil
}

func (i *Inverter) disconnect() error {
	return i.client.Close()
}

func (i *Inverter) Run(ctx context.Context, ch chan<- Stats, powerRate <-chan uint16) error {
	// Open the connection (retrying until the inverter is reachable or the
	// context is cancelled) and ensure it is closed when Run returns.
	if err := i.connectWithRetry(ctx); err != nil {
		return err
	}
	defer i.disconnect()

	// Upon start make sure the inverter keeps power-control writes in RAM
	// rather than persisting them, to avoid wearing out its non-volatile
	// memory when the active power rate is changed frequently.
	if err := i.ensureCmdMemoryDisabled(); err != nil {
		slog.Warn(fmt.Sprintf("Failed to disable PF CMD memory on inverter upon start: %v", err))
	}

	// Seed the active power rate from the inverter once. After this we track the
	// value we command rather than re-reading it (see getStats). Fall back to
	// the documented default of 100% if the inverter can't be read yet.
	i.activePowerRate = 100
	if rate, err := i.getActivePowerRate(); err != nil {
		slog.Warn(fmt.Sprintf("Failed to read initial active power rate: %v", err))
	} else {
		i.activePowerRate = rate
	}

	// Upon start always check (and set when needed) the time on the inverter.
	if err := i.checkSetTime(); err != nil {
		slog.Warn(fmt.Sprintf("Failed to CheckSetTime on inverter upon start: %v", err))
	}

	// Set last known state to 'waiting' upon start reading. This is
	// the state of the inverter when it is turned off and the
	// registers cannot be read. Start with this state when it is not
	// known yet.
	lastInverterState := InverterStateCodes[0]

	errCnt := 0

	slog.Info("Start inverter handling")

	statsTicker := time.NewTicker(2 * time.Second)
	defer statsTicker.Stop()
	checkTimeTicker := time.NewTicker(30 * time.Minute)
	defer checkTimeTicker.Stop()

	for {
		select {
		case <-statsTicker.C:
			stats, err := i.getStats()
			if err != nil {
				isTimeout := errors.Is(err, modbus.ErrRequestTimedOut)

				if lastInverterState == InverterStateCodes[0] && isTimeout {
					// Inverter is in 'waiting state' and modbus registers
					// can only be read when the status is changed to 'normal'.
					slog.Debug(
						fmt.Sprintf("Last known inverter state is 'waiting' and got error: %v. Sleep for a minute and try again.",
							err,
						),
					)
					select {
					case <-time.After(1 * time.Minute):
					case <-ctx.Done():
						return i.stop(ctx)
					}
					continue
				}

				slog.Warn(fmt.Sprintf("Got error while retrieving modbus registers: %v", err))

				// A timeout can be a transient glitch, so allow a few before
				// reconnecting. Any other error (e.g. a 'broken pipe' when the
				// connection is dropped) means the connection is gone, so
				// reconnect right away.
				errCnt++
				if !isTimeout || errCnt >= 10 {
					slog.Warn(fmt.Sprintf("Reconnecting to inverter after %d error(s)", errCnt))
					if err := i.reconnect(ctx); err != nil {
						return err // ctx cancelled during reconnect
					}
					errCnt = 0
				}
				continue
			}
			select {
			case ch <- stats:
			case <-ctx.Done():
				return i.stop(ctx)
			}
			lastInverterState = stats.State
			errCnt = 0

		case <-checkTimeTicker.C:
			// Only try to check and set time when inverter is in 'normal' state
			if lastInverterState == InverterStateCodes[1] {
				if err := i.checkSetTime(); err != nil {
					slog.Error(fmt.Sprintf("CheckSetTime on inverter failed: %v", err))
					continue
				}
			}

		case rate := <-powerRate:
			// Handled here (not in the MQTT callback) so all modbus client
			// access stays in this single goroutine, avoiding a data race on
			// i.client.

			// Make sure writes stay in RAM before touching the power rate. If
			// the startup attempt failed (e.g. inverter was in 'waiting' state)
			// retry now, and skip the write if we still can't confirm it, so we
			// never persist to non-volatile memory.
			if !i.cmdMemoryDisabled {
				if err := i.ensureCmdMemoryDisabled(); err != nil {
					slog.Error(fmt.Sprintf("Refusing power rate change: could not disable PF CMD memory: %v", err))
					continue
				}
			}

			if err := i.setActivePowerRate(rate); err != nil {
				slog.Error(fmt.Sprintf("Failed to set active power rate to %d: %v", rate, err))
			}

		case <-ctx.Done():
			return i.stop(ctx)
		}
	}
}

// reconnect closes the current connection and re-establishes it. Used after
// repeated read timeouts.
func (i *Inverter) reconnect(ctx context.Context) error {
	i.disconnect()
	return i.connectWithRetry(ctx)
}

// connectWithRetry connects to the inverter, retrying every 10s until it
// succeeds or the context is cancelled (in which case it returns ctx.Err()).
func (i *Inverter) connectWithRetry(ctx context.Context) error {
	for {
		if err := i.connect(); err == nil {
			return nil
		}

		slog.Warn("Connect to inverter failed, retrying in 10s")
		select {
		case <-time.After(10 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (i *Inverter) stop(ctx context.Context) error {
	slog.Info("Context canceled, stop inverter handling")
	return ctx.Err()
}

func (i *Inverter) getStats() (Stats, error) {
	data, err := i.client.ReadRegisters(0, 41, modbus.INPUT_REGISTER)
	if err != nil {
		return Stats{}, fmt.Errorf("failed to read registers 0-41: %w", err)
	}

	stats := parseStats(data, time.Now().UTC().Format("2006-01-02 15:04:05"))

	// Report the last known active power rate. We can't re-read register 3 here:
	// with PF CMD memory disabled (register 2 = 0) a write to register 3 takes
	// effect in RAM but the register read still returns the persisted value, so
	// reading it back would wrongly report 100. Instead the cache is seeded once
	// at startup and updated whenever we set the rate.
	stats.ActivePowerRate = i.activePowerRate

	return stats, nil
}

// parseStats decodes a block of 41 input registers (starting at register 0)
// into a Stats struct. timestamp is the value used for the Timestamp field.
func parseStats(data []uint16, timestamp string) Stats {
	stats := Stats{
		Timestamp:         timestamp,
		State:             InverterStateCodes[data[0]],
		PVInputPower:      (uint32(data[1])<<16 | uint32(data[2])) / 10,
		PV1InputVolt:      data[3] / 10,
		PV1InputCurrent:   data[4] / 10,
		PV1InputWatt:      (uint32(data[5])<<16 | uint32(data[6])) / 10,
		PV2InputVolt:      data[7] / 10,
		PV2InputCurrent:   data[8] / 10,
		PV2InputWatt:      (uint32(data[9])<<16 | uint32(data[10])) / 10,
		ACWatt:            (uint32(data[11])<<16 | uint32(data[12])) / 10,
		ACFrequency:       data[13] / 100,
		AC1Volt:           data[14] / 10,
		AC1Current:        data[15] / 10,
		AC1Watt:           (uint32(data[16])<<16 | uint32(data[17])) / 10,
		AC2Volt:           data[18] / 10,
		AC2Current:        data[19] / 10,
		AC2Watt:           (uint32(data[20])<<16 | uint32(data[21])) / 10,
		AC3Volt:           data[22] / 10,
		AC3Current:        data[23] / 10,
		AC3Watt:           (uint32(data[24])<<16 | uint32(data[25])) / 10,
		TotalTodayWatt:    (uint32(data[26])<<16 | uint32(data[27])) * 100,
		TotalAllTimeWatt:  (uint32(data[28])<<16 | uint32(data[29])) * 100,
		TotalWorkTimeSecs: (uint32(data[30])<<16 | uint32(data[31])) / 2,
		Temperature:       data[32] / 10,
		FaultCode:         data[40],
	}

	return stats
}

func (i *Inverter) checkSetTime() error {
	inverterTime, err := i.getTime()
	if err != nil {
		return err
	}

	now := time.Now().Local()
	diffSecs := math.Abs(now.Sub(inverterTime).Seconds())

	// Check if time difference exceeds 30 seconds
	if diffSecs > 30 {
		slog.Info(
			fmt.Sprintf("Time difference(%d secs) on inverter(%s) exceeds threshold of 30 seconds. Updating to time(%s) now!",
				int(diffSecs),
				inverterTime,
				now,
			),
		)
		if err := i.setTime(inverterTime, now); err != nil {
			return err
		}
	}

	return nil
}

func (i *Inverter) setTime(inverterTime time.Time, newTime time.Time) error {
	if inverterTime.Second() != newTime.Second() {
		slog.Info(
			fmt.Sprintf(
				"Updating inverter time(seconds) from %d to %d",
				inverterTime.Second(),
				newTime.Second(),
			),
		)
		if err := i.client.WriteRegister(50, uint16(newTime.Second())); err != nil {
			return fmt.Errorf("failed to update time(second): %v", err)
		}
	}

	if inverterTime.Minute() != newTime.Minute() {
		slog.Info(
			fmt.Sprintf(
				"Updating inverter time(minutes) from %d to %d",
				inverterTime.Minute(),
				newTime.Minute(),
			),
		)
		if err := i.client.WriteRegister(49, uint16(newTime.Minute())); err != nil {
			return fmt.Errorf("failed to update time(minute): %v", err)
		}
	}

	if inverterTime.Hour() != newTime.Hour() {
		slog.Info(
			fmt.Sprintf(
				"Updating inverter time(hours) from %d to %d",
				inverterTime.Hour(),
				newTime.Hour(),
			),
		)
		if err := i.client.WriteRegister(48, uint16(newTime.Hour())); err != nil {
			return fmt.Errorf("failed to update time(hour): %v", err)
		}
	}

	if inverterTime.Day() != newTime.Day() {
		slog.Info(
			fmt.Sprintf(
				"Updating inverter time(day) from %d to %d",
				inverterTime.Day(),
				newTime.Day(),
			),
		)
		if err := i.client.WriteRegister(47, uint16(newTime.Day())); err != nil {
			return fmt.Errorf("failed to update time(day): %v", err)
		}
	}

	if inverterTime.Month() != newTime.Month() {
		slog.Info(
			fmt.Sprintf(
				"Updating inverter time(month) from %d to %d",
				inverterTime.Month(),
				newTime.Month(),
			),
		)
		if err := i.client.WriteRegister(46, uint16(newTime.Month())); err != nil {
			return fmt.Errorf("failed to update time(month): %v", err)
		}
	}

	if inverterTime.Year() != newTime.Year() {
		slog.Info(
			fmt.Sprintf(
				"Updating inverter time(year) from %d to %d",
				inverterTime.Year(),
				newTime.Year(),
			),
		)
		if err := i.client.WriteRegister(45, uint16(newTime.Year())); err != nil {
			// Updating the year probably returns an 'Illegal instruction' error.
			// Log it and continue.
			slog.Warn(
				fmt.Sprintf(
					"failed to update time(year): %v. This is probably not supported via modbus :(",
					err,
				),
			)
		}
	}

	return nil
}

func (i *Inverter) getTime() (time.Time, error) {
	data, err := i.client.ReadRegisters(45, 6, modbus.HOLDING_REGISTER)
	if err != nil {
		slog.Error(err.Error())
		return time.Time{}, err
	}

	// Convert to time.Time
	t := time.Date(
		int(data[0]),
		time.Month(data[1]),
		int(data[2]),
		int(data[3]),
		int(data[4]),
		int(data[5]),
		0, // nanoseconds
		time.Local,
	)
	slog.Debug(fmt.Sprintf("Current time on inverter is %v", t))

	return t, nil
}

// setActivePowerRate can be used to limit the maximum AC output of the inverter.
// This is a percentage of the maximum output that the inverter can deliver.
// E.g. when the maximum AC output of the inverter is 3600W:
// 100%: 3600W
// 50%:  1800W
// 0%:   0W
func (i *Inverter) setActivePowerRate(percentage uint16) error {
	slog.Info(fmt.Sprintf("Change output power percentage to %d", percentage))

	if err := i.client.WriteRegister(3, percentage); err != nil {
		return fmt.Errorf(
			"failed to set active power percentage(%d): %w",
			percentage, err,
		)
	}
	i.activePowerRate = percentage

	return nil
}

// getActivePowerRate reads the currently configured active power rate (the same
// holding register that setActivePowerRate writes to). The value is a
// percentage of the inverter's maximum AC output (0-100).
func (i *Inverter) getActivePowerRate() (uint16, error) {
	data, err := i.client.ReadRegisters(3, 1, modbus.HOLDING_REGISTER)
	if err != nil {
		return 0, fmt.Errorf("failed to read active power percentage: %w", err)
	}

	return data[0], nil
}

// ensureCmdMemoryDisabled makes sure holding register 2 ("PF CMD memory state")
// is 0. When it is 1 the inverter persists every write to the power control
// registers (3 Active P Rate, 4 Reactive P Rate, 5 Power factor, 99) to its
// non-volatile memory, which wears it out. With it set to 0 those writes are
// kept in RAM only, so frequently changing the active power rate is safe. The
// value survives across reads, so once confirmed we don't touch it again.
func (i *Inverter) ensureCmdMemoryDisabled() error {
	data, err := i.client.ReadRegisters(2, 1, modbus.HOLDING_REGISTER)
	if err != nil {
		return fmt.Errorf("failed to read PF CMD memory state: %w", err)
	}

	if data[0] != 0 {
		slog.Info("PF CMD memory state is enabled; disabling it so power control writes stay in RAM")
		if err := i.client.WriteRegister(2, 0); err != nil {
			return fmt.Errorf("failed to disable PF CMD memory state: %w", err)
		}
	} else {
		slog.Info("PF CMD memory state is disabled; This is good and avoids flash wear.")
	}

	i.cmdMemoryDisabled = true

	return nil
}
