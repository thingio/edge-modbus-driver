package main

import (
	"github.com/thingio/edge-device-driver/pkg/startup"
	"github.com/thingio/edge-modbus-driver/driver"
)

func main() {
	go startup.Startup(driver.ModbusTCP, driver.NewModbusTCPTwin)
	go startup.Startup(driver.ModbusRTU, driver.NewModbusRTUTwin)

	select {}
}
