# Go-Growatt
Go-Growatt is used to read various modbus registers from a Growatt inverter and publish them to a MQTT server.

Upon startup a configuration file must be supplied as first commandline argument. The file `config.dist.yaml` as a template.

Example usage:
```bash
build/go-growatt-pi config.yaml
```