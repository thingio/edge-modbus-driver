module github.com/thingio/edge-modbus-driver

replace (
	github.com/thingio/edge-device-driver v0.2.1 => ../edge-device-driver
	github.com/thingio/edge-device-std v0.2.1 => ../edge-device-std
)

require (
	github.com/dpapathanasiou/go-modbus v0.0.0-20200626124227-44739634e73d
	github.com/patrickmn/go-cache v2.1.0+incompatible
	github.com/spf13/afero v1.8.0 // indirect
	github.com/spf13/viper v1.10.1 // indirect
	github.com/tarm/goserial v0.0.0-20151007205400-b3440c3c6355 // indirect
	github.com/thingio/edge-device-driver v0.2.1
	github.com/thingio/edge-device-std v0.2.1
	golang.org/x/net v0.0.0-20220111093109-d55c255bac03 // indirect
	golang.org/x/sys v0.0.0-20220111092808-5a964db01320 // indirect
)

go 1.16
