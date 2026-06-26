# Go-Growatt
Go-Growatt is used to read various modbus registers from a Growatt inverter and publish them to a MQTT server.

Upon startup a configuration file must be supplied as first commandline argument. The file `config.dist.yaml` as a template.

Example usage:
```bash
build/go-growatt-pi config.yaml
```

## Testing
When the inverter is connected via a serial connection to a computer and
you want to run go-growatt on another computer you can expose the serial
connection over TCP with the python `ser2net.py` script.

On the computer where the inverter is connected:
```bash
# Stop any other process which is reading from the serial port
# Then start ser2net.py
./ser2net.py
```

Use the correct address in config.yaml on the computer where go-growatt will be started:
```
# example.local should be replaced with the IP or hostname of the other computer
rtuovertcp://example.local:5000
```
