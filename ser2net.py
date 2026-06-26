#!/usr/bin/env python3
"""Minimal serial <-> TCP bridge (stdlib only) for Modbus RTU over TCP.

Listens on a TCP port and shuttles raw bytes to/from a serial device, so a
remote machine can reach the inverter via rtuovertcp://host:port. One client
at a time (Modbus is single-master); a new connection replaces the old one.

Usage: python3 serial_bridge.py [device] [tcp_port] [baud]
Defaults: /dev/ttyUSB0  5000  9600
"""

import os
import sys
import select
import socket
import termios

BAUD_RATES = {
    9600: termios.B9600,
    19200: termios.B19200,
    38400: termios.B38400,
    57600: termios.B57600,
    115200: termios.B115200,
}


def open_serial(path, baud):
    fd = os.open(path, os.O_RDWR | os.O_NOCTTY | os.O_NONBLOCK)
    iflag, oflag, cflag, lflag, ispeed, ospeed, cc = termios.tcgetattr(fd)
    # 8N1, raw, no flow control, ignore modem control lines.
    iflag = 0
    oflag = 0
    lflag = 0
    cflag = termios.CS8 | termios.CLOCAL | termios.CREAD
    ispeed = ospeed = BAUD_RATES[baud]
    cc = list(cc)
    cc[termios.VMIN] = 0
    cc[termios.VTIME] = 0
    termios.tcsetattr(
        fd, termios.TCSANOW, [iflag, oflag, cflag, lflag, ispeed, ospeed, cc]
    )
    termios.tcflush(fd, termios.TCIOFLUSH)
    return fd


def write_all(fd, data):
    while data:
        try:
            data = data[os.write(fd, data) :]
        except BlockingIOError:
            select.select([], [fd], [])


def main():
    device = sys.argv[1] if len(sys.argv) > 1 else "/dev/ttyUSB0"
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 5000
    baud = int(sys.argv[3]) if len(sys.argv) > 3 else 9600
    if baud not in BAUD_RATES:
        sys.exit(f"unsupported baud {baud}; pick one of {sorted(BAUD_RATES)}")

    serial_fd = open_serial(device, baud)

    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("0.0.0.0", port))
    srv.listen(1)
    srv.setblocking(False)
    print(
        f"Bridging {device} <-> TCP :{port} ({baud} 8N1). Ctrl-C to stop.", flush=True
    )

    client = None
    try:
        while True:
            watch = [srv, serial_fd] + ([client] if client else [])
            readable, _, _ = select.select(watch, [], [])
            for r in readable:
                if r is srv:
                    conn, addr = srv.accept()
                    conn.setblocking(False)
                    if client is not None:
                        client.close()
                    client = conn
                    termios.tcflush(serial_fd, termios.TCIOFLUSH)
                    print(f"Client connected: {addr[0]}:{addr[1]}", flush=True)
                elif client is not None and r is client:
                    try:
                        data = client.recv(4096)
                    except OSError:
                        data = b""
                    if data:
                        write_all(serial_fd, data)
                    else:
                        print("Client disconnected", flush=True)
                        client.close()
                        client = None
                elif r == serial_fd:
                    try:
                        data = os.read(serial_fd, 4096)
                    except BlockingIOError:
                        data = b""
                    if data and client is not None:
                        try:
                            client.sendall(data)
                        except OSError:
                            client.close()
                            client = None
                # else: stale fd (just-replaced client), ignore
    except KeyboardInterrupt:
        pass
    finally:
        if client is not None:
            client.close()
        srv.close()
        os.close(serial_fd)


if __name__ == "__main__":
    main()
