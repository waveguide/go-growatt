package inverter

import (
	"context"
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
}

type Inverter struct {
	Address  string
	BaudRate int
	client   *modbus.ModbusClient
}

func (i *Inverter) Connect() error {
	slog.Info("Connecting to inverter")

	// Create and configure client
	c, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:      fmt.Sprintf("rtu://%s", i.Address),
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

func (i *Inverter) Disconnect() error {
	return i.client.Close()
}

func (i *Inverter) Read(ctx context.Context, ch chan<- Stats) error {
	// Set last known state to 'waiting' upon start reading. This is
	// the state of the inverter when it is turned off and the
	// registers cannot be read. Start with this state when it is not
	// known yet.
	lastInverterState := InverterStateCodes[0]

	var IsTimeoutError bool
	timeoutCnt := 0

	slog.Info("Start reading from inverter")

	statsTicker := time.NewTicker(2 * time.Second)
	defer statsTicker.Stop()
	checkTimeTicker := time.NewTicker(30 * time.Minute)
	defer checkTimeTicker.Stop()

	for {
		select {
		case <-statsTicker.C:
			stats, err := i.getStats()
			if err != nil {
				if err.Error() == "request timed out" {
					IsTimeoutError = true
				} else {
					IsTimeoutError = false
				}

				if lastInverterState == InverterStateCodes[0] && IsTimeoutError {
					// Inverter is in 'waiting state' and modbus registers
					// can only be read when the status is changed to 'normal'.
					slog.Debug(
						fmt.Sprintf("Last known inverter state is 'waiting' and got error: %v. Sleep for a minute and try again.",
							err,
						),
					)
					time.Sleep(1 * time.Minute)
				} else {
					slog.Warn(fmt.Sprintf("Got error while retrieving modbus registers: %v", err))
					if IsTimeoutError {
						timeoutCnt += 1

						if timeoutCnt > 10 {
							slog.Warn(fmt.Sprintf("Got %d subsequent read timeouts. Reconnect to inverter.", timeoutCnt))
							i.Disconnect()
							i.Connect()
						}
					}
				}
				continue
			}
			ch <- stats
			lastInverterState = stats.State
			timeoutCnt = 0

		case <-checkTimeTicker.C:
			if err := i.CheckSetTime(); err != nil {
				slog.Error(fmt.Sprintf("CheckSetTime on inverter failed: %v", err))
				continue
			}

		case <-ctx.Done():
			slog.Info("Context canceled, stop reading from inverter")
			i.Disconnect()
			return ctx.Err()
		}
	}
}

func (i *Inverter) getStats() (Stats, error) {
	data, err := i.client.ReadRegisters(0, 41, modbus.INPUT_REGISTER)
	if err != nil {
		return Stats{}, err
	}

	stats := Stats{
		Timestamp:         time.Now().UTC().Format("2006-01-02 15:04:05"),
		State:             InverterStateCodes[data[0]],
		PVInputPower:      (uint32(data[1])<<16 | uint32(data[2])) / 10,
		PV1InputVolt:      data[3] / 10,
		PV1InputCurrent:   data[4] / 10,
		PV1InputWatt:      (uint32(data[5])<<16 | uint32(data[5])) / 10,
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

	return stats, nil
}

func (i *Inverter) CheckSetTime() error {
	inverterTime, err := i.GetTime()
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

func (i *Inverter) GetTime() (time.Time, error) {
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
