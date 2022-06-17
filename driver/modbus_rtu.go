package driver

import (
	"context"
	"encoding/binary"
	"fmt"
	modbusclient "github.com/dpapathanasiou/go-modbus"
	"github.com/thingio/edge-device-std/errors"
	"github.com/thingio/edge-device-std/logger"
	"github.com/thingio/edge-device-std/models"
	"io"
	"strconv"
	"sync"
)

type modbusRTUDriver struct {
	*modbusRTUDeviceConf
	tsID   int                // transaction id
	pid    string             // product id
	did    string             // device id
	conn   io.ReadWriteCloser // tcp connection
	lock   sync.Mutex
	once   sync.Once // ensure there is only one reconnect goroutine
}

type modbusRTUDeviceConf struct {
	serialDevice string
	slave        int
	baudRate     int
	timeoutMS    int
	startAddr    int
	numBytes     int
	pauseResp    int
}

type modbusRTUTwin struct {
	*modbusRTUDriver

	product *models.Product
	device  *models.Device

	properties map[models.ProductPropertyID]*models.ProductProperty // for property's reading and writing

	lg *logger.Logger

	ctx    context.Context
	cancel context.CancelFunc
}

func NewModbusRTUDriver(device *models.Device) (*modbusRTUDriver, error) {
	conf, err := parseModbusRTUDeviceConf(device)
	if err != nil {
		return nil, err
	}
	return &modbusRTUDriver{
		modbusRTUDeviceConf: conf,
		tsID:                0,
		pid:                 device.ProductID,
		did:                 device.ID,
		conn:                nil,
		lock:                sync.Mutex{},
		once:                sync.Once{},
	}, nil
}
func parseModbusRTUDeviceConf(device *models.Device) (*modbusRTUDeviceConf, error) {
	dc := &modbusRTUDeviceConf{}
	for k, v := range device.DeviceProps {
		switch k {
		case ModbusRTUDeviceSerialDevice:
			dc.serialDevice = v
		case ModbusRTUDeviceBaudRate:
			baudRate, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("baudRate [%v] error", v), nil)
			}
			dc.baudRate = int(baudRate)
		case ModbusRTUDeviceStartAddr:
			s, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("startAddr [%v] error", v), nil)
			}
			dc.startAddr = int(s)
		case ModbusRTUDeviceNumBytes:
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("numBytes [%v] error", v), nil)
			}
			dc.numBytes = int(n)
		case ModbusRTUDeviceResponsePause:
			p, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("pauseResp [%v] error", v), nil)
			}
			dc.pauseResp = int(p)
		case ModbusRTUDeviceTimeout:
			t, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("timeout [%v] error", v), nil)
			}
			dc.timeoutMS = int(t)
		case ModbusRTUDeviceSlave:
			s, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("slave [%v] error", v), nil)
			}
			dc.slave = int(s)
		}
	}
	if dc.serialDevice == "" || dc.baudRate == 0 || dc.timeoutMS == 0 {
		return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("RTU initial modbusRTUConf[%+v] error", dc), nil)
	}
	return dc, nil
}

func NewModbusRTUTwin(product *models.Product, device *models.Device) (models.DeviceTwin, error) {
	if product == nil {
		return nil, errors.NewCommonEdgeError(errors.DeviceTwin, "Product is nil", nil)
	}
	if device == nil {
		return nil, errors.NewCommonEdgeError(errors.DeviceTwin, "Device is nil", nil)
	}
	twin := &modbusRTUTwin{
		product: product,
		device:  device,
	}
	return twin, nil
}

func (m *modbusRTUTwin) Initialize(lg *logger.Logger) error {
	m.lg = lg

	rtuDriver, err := NewModbusRTUDriver(m.device)
	if err != nil {
		return errors.NewCommonEdgeError(errors.DeviceTwin, "failed to initialize ModbusRTU driver", err)
	}
	m.modbusRTUDriver = rtuDriver

	m.properties = make(map[models.ProductPropertyID]*models.ProductProperty)
	for _, property := range m.product.Properties {
		m.properties[property.Id] = property
	}
	return nil
}

func (m *modbusRTUTwin) Start(ctx context.Context) error {
	m.ctx, m.cancel = context.WithCancel(ctx)

	conn, err := modbusclient.ConnectRTU(m.serialDevice, m.baudRate)
	if err != nil {
		m.lg.Errorf("modbusTwin.Start Error: RTU Connection: %+v %+v, err: %+v", m.serialDevice, m.baudRate, err)
		return err
	}
	m.conn = conn

	return nil
}

func (m *modbusRTUTwin) Stop(force bool) error {
	m.cancel()

	if m.conn != nil {
		if err := m.conn.Close(); err != nil {
			m.lg.Info("Stop ModbusTwin RTUDriver Error: %v:%V, err: %+v", m.serialDevice, m.baudRate, err)
			return err
		}
	}
	return nil
}

func (m *modbusRTUTwin) HealthCheck() (*models.DeviceStatus, error) {
	// TODO
	return nil, nil
}

func (m *modbusRTUTwin) Read(propertyID models.ProductPropertyID) (map[models.ProductPropertyID]*models.DeviceData, error) {
	res := make(map[models.ProductPropertyID]*models.DeviceData)
	property, ok := m.properties[propertyID]
	if !ok {
		return nil, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("the property[%s] hasn't been ready", property.Id), nil)
	}
	m.lock.Lock()
	resp, err := modbusclient.RTURead(
		m.conn,
		byte(m.slave),
		ReadFuncCodes[property.AuxProps[ModbusExtendRgsType]],
		uint16(m.startAddr),
		uint16(m.numBytes),
		m.timeoutMS,
		false,
	)
	m.lock.Unlock()
	if err != nil {
		m.lg.Errorf("modbus HardRead Error: propertyID %+v, error %+v", propertyID, err)
		return res, errors.NewCommonEdgeError(errors.BadRequest, "read modbus RTU device failed", nil)
	}
	value, err := pud2Data(property.FieldType, binary.BigEndian, m.pdu(resp))
	if err != nil {
		return res, err
	}
	res[propertyID], _ = models.NewDeviceData(propertyID, property.FieldType, value)
	return res, nil
}
func (d *modbusRTUDriver) pdu(frame []byte) []byte {
	return frame[ModbusMBAPLength:]
}

func (m *modbusRTUTwin) Write(propertyID models.ProductPropertyID, values map[models.ProductPropertyID]*models.DeviceData) error {
	property, ok := m.properties[propertyID]
	if !ok {
		return errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("property %s of modbus RTU device not found", propertyID), nil)
	}
	var follow uint16
	var funcCode uint8
	rgsAddr, _ := strconv.ParseUint(property.AuxProps[ModbusExtendRgsAddr], 10, 16)
	rgsCount, _ := strconv.ParseUint(property.AuxProps[ModbusExtendRgsCount], 10, 16)
	switch property.AuxProps[ModbusExtendRgsType] {
	case ModbusRgsTypeCoil:
		follow = (uint16(rgsCount) + 7) >> 3 // 1 bit per coil
		funcCode = WriteFuncCodes[ModbusRgsTypeCoil]
	case ModbusRgsTypeHoldingRegister:
		follow = 2 * uint16(rgsCount) // 2 bytes per register
		funcCode = WriteFuncCodes[ModbusRgsTypeHoldingRegister]
	default:
		return errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("register type %s in modbus device has not supported yet", property.AuxProps[ModbusExtendRgsType]), nil)
	}
	if 4+follow > ModbusPDUMaxLength {
		return errors.NewCommonEdgeError(errors.Driver, "the length of modbus pdu out of range, maximum is 253", nil)
	}

	head := getDataHead(uint16(rgsAddr), uint16(rgsCount))
	bs, err := data2Bytes(property.FieldType, binary.BigEndian, values[propertyID].Value, int(follow))
	if err != nil {
		return err
	}

	m.lock.Lock()
	_, err = modbusclient.RTUWrite(
		m.conn,
		byte(m.slave),
		funcCode,
		uint16(m.startAddr),
		uint16(m.numBytes),
		append(head, bs...),
		m.timeoutMS,
		false,
	)
	m.lock.Unlock()
	if err != nil {
		return errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("%+v write modbus RTU device failed", err), nil)
	}
	return nil
}

func (m *modbusRTUTwin) Subscribe(eventID models.ProductEventID, bus chan<- *models.DeviceDataWrapper) error {
	return errors.NewCommonEdgeError(errors.MethodNotAllowed, fmt.Sprintf("ModbusRTU device does not support events' subscribing"), nil)
}

func (m *modbusRTUTwin) Call(methodID models.ProductMethodID, ins map[models.ProductPropertyID]*models.DeviceData) (outs map[models.ProductPropertyID]*models.DeviceData, err error) {
	return nil, errors.NewCommonEdgeError(errors.MethodNotAllowed, fmt.Sprintf("ModbusRTU device does not support methods' calling"), nil)
}
