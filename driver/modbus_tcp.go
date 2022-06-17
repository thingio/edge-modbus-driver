package driver

import (
	"context"
	"encoding/binary"
	"fmt"
	modbusclient "github.com/dpapathanasiou/go-modbus"
	"github.com/thingio/edge-device-std/errors"
	"github.com/thingio/edge-device-std/logger"
	"github.com/thingio/edge-device-std/models"
	"net"
	"strconv"
	"sync"
	"time"
)

type modbusTCPDriver struct {
	*modbusTCPDeviceConf
	tsID int      // transaction id
	pid  string   // product id
	did  string   // device id
	conn net.Conn // tcp connection
	lock sync.Mutex
	once sync.Once // ensure there is only one reconnect goroutine
}

type modbusTCPDeviceConf struct {
	ip           string
	slave        byte
	port         int
	timeoutMS    int
	serialBridge bool // 是否为通过网关连接的串行设备,若是则UnitID为SlaveID,否则为0x00(非法SlaveID)
}

type modbusTCPTwin struct {
	*modbusTCPDriver

	product *models.Product
	device  *models.Device

	properties map[models.ProductPropertyID]*models.ProductProperty // for property's reading and writing

	lg *logger.Logger

	ctx    context.Context
	cancel context.CancelFunc
}

func (d *modbusTCPDriver) pdu(frame []byte) []byte {
	return frame[ModbusMBAPLength:]
}

func newModbusTCPDriver(device *models.Device) (*modbusTCPDriver, error) {
	conf, err := parseModbusTcpDeviceConf(device)
	if err != nil {
		return nil, err
	}
	return &modbusTCPDriver{
		modbusTCPDeviceConf: conf,
		tsID:                0,
		pid:                 device.ProductID,
		did:                 device.ID,
		conn:                nil,
		lock:                sync.Mutex{},
		once:                sync.Once{},
	}, nil
}
func parseModbusTcpDeviceConf(device *models.Device) (*modbusTCPDeviceConf, error) {
	dc := &modbusTCPDeviceConf{}
	for k, v := range device.DeviceProps {
		switch k {
		case ModbusTCPDeviceIP:
			dc.ip = v
		case ModbusTCPDevicePort:
			port, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("port [%v] error", v), nil)
			}
			dc.port = int(port)
		case ModbusTCPDeviceSerialBridge:
			dc.serialBridge = v == "true"
		case ModbusTCPDeviceTimeout:
			t, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("timeout [%v] error", v), nil)
			}
			dc.timeoutMS = int(t)
		case ModbusTCPDeviceSlave:
			slave, err := strconv.ParseUint(v, 10, 8)
			if err != nil {
				return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("port [%v] error", v), nil)
			}
			dc.slave = byte(slave)
		}
	}
	if dc.ip == "" || dc.port == 0 || dc.timeoutMS == 0 {
		return dc, errors.NewCommonEdgeError(errors.Configuration, fmt.Sprintf("tcp initial modbusTcpConf[%+v] error", dc), nil)
	}
	return dc, nil
}

func NewModbusTCPTwin(product *models.Product, device *models.Device) (models.DeviceTwin, error) {
	if product == nil {
		return nil, errors.NewCommonEdgeError(errors.DeviceTwin, "Product is nil", nil)
	}
	if device == nil {
		return nil, errors.NewCommonEdgeError(errors.DeviceTwin, "Device is nil", nil)
	}
	twin := &modbusTCPTwin{
		product: product,
		device:  device,
	}
	return twin, nil
}

func (m *modbusTCPTwin) Initialize(lg *logger.Logger) error {
	m.lg = lg

	tcpDriver, err := newModbusTCPDriver(m.device)
	if err != nil {
		return errors.NewCommonEdgeError(errors.DeviceTwin, "failed to initialize ModbusTCP driver", err)
	}
	m.modbusTCPDriver = tcpDriver

	m.properties = make(map[models.ProductPropertyID]*models.ProductProperty)
	for _, property := range m.product.Properties {
		m.properties[property.Id] = property
	}
	return nil
}

func (m *modbusTCPTwin) Start(ctx context.Context) error {
	m.ctx, m.cancel = context.WithCancel(ctx)

	addr, err := net.ResolveTCPAddr("tcp4", fmt.Sprintf("%s:%d", m.ip, m.port))
	if err != nil {
		m.lg.Errorf("modbusTwin.Start Error: illegal addr: %+v, err: %+v", addr, err)
		return err
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	conn, err := net.DialTimeout("tcp", addr.String(), time.Duration(m.timeoutMS)*time.Millisecond)
	if err != nil {
		m.lg.Errorf("modbusTwin.Start Error: TCP Connection addr: %+v, err: %+v", addr, err)
		return err
	}
	m.conn = conn
	return nil
}

func (m *modbusTCPTwin) Stop(force bool) error {
	m.cancel()

	m.lock.Lock()
	defer m.lock.Unlock()
	if m.conn != nil {
		if err := m.conn.Close(); err != nil {
			m.lg.Info("Stop ModbusTwin TCPDriver Error: %s:%s, err: %+v", m.ip, m.port, err)
			return err
		}
	}
	return nil
}

func (m *modbusTCPTwin) HealthCheck() (*models.DeviceStatus, error) {
	frame := modbusclient.TCPFrame{
		TimeoutInMilliseconds:  2000,
		DebugTrace:             false,
		TransactionID:          0,
		FunctionCode:           1,
		EthernetToSerialBridge: false,
		SlaveAddress:           1,
		Data:                   []byte{0, 1, 0, 1},
	}
	if m.conn != nil {
		m.lock.Lock()
		_, err := frame.TransmitAndReceive(m.conn)
		m.lock.Unlock()
		if err != nil {
			return &models.DeviceStatus{
				Device:      m.device,
				State:       models.DeviceStateReconnecting,
				StateDetail: "重新连接",
			}, nil
		}
		return &models.DeviceStatus{
			Device:      m.device,
			State:       models.DeviceStateConnected,
			StateDetail: "连接正常",
		}, nil
	}
	return &models.DeviceStatus{
		Device:      m.device,
		State:       models.DeviceStateReconnecting,
		StateDetail: "重新连接",
	}, nil
}

func (m *modbusTCPTwin) Read(propertyID models.ProductPropertyID) (map[models.ProductPropertyID]*models.DeviceData, error) {
	var err error

	values := make(map[models.ProductPropertyID]*models.DeviceData)
	if propertyID == models.DeviceDataMultiPropsID {
		for _, property := range m.properties {
			values[property.Id], err = m.read(property)
			if err != nil {
				return nil, err
			}
		}
	} else {
		property, ok := m.properties[propertyID]
		if !ok {
			return nil, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("undefined property: %s", property.Id), nil)
		}
		values[propertyID], err = m.read(property)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}
func (m *modbusTCPTwin) read(property *models.ProductProperty) (*models.DeviceData, error) {
	rgsAddr, _ := strconv.ParseUint(property.AuxProps[ModbusExtendRgsAddr], 10, 16)
	rgsCount, _ := strconv.ParseUint(property.AuxProps[ModbusExtendRgsCount], 10, 16)
	h := getDataHead(uint16(rgsAddr), uint16(rgsCount))
	m.lock.Lock()
	resp, err := modbusclient.TCPRead(
		m.conn,
		m.modbusTCPDeviceConf.timeoutMS,
		m.tsID,
		ReadFuncCodes[property.AuxProps[ModbusExtendRgsType]],
		m.modbusTCPDeviceConf.serialBridge,
		m.modbusTCPDeviceConf.slave,
		h,
		false)
	if err != nil {
		return nil, errors.NewCommonEdgeError(errors.Internal, "read modbus TCP device failed", nil)
	}
	m.tsID++
	m.lock.Unlock()
	value, err := pud2Data(property.FieldType, binary.BigEndian, m.pdu(resp))
	if err != nil {
		return nil, err
	}
	return models.NewDeviceData(property.Id, property.FieldType, value)
}

func (m *modbusTCPTwin) Write(propertyID models.ProductPropertyID, values map[models.ProductPropertyID]*models.DeviceData) error {
	property, ok := m.properties[propertyID]
	if !ok {
		return errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("property %s of modbus tcp device not found", propertyID), nil)
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
	_, err = modbusclient.TCPWrite(
		m.conn,
		m.modbusTCPDeviceConf.timeoutMS,
		m.tsID,
		funcCode,
		m.modbusTCPDeviceConf.serialBridge,
		m.modbusTCPDeviceConf.slave,
		append(head, bs...),
		false)
	m.tsID++
	m.lock.Unlock()
	if err != nil {
		return errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("failed to write modbus tcp device"), err)
	}
	return nil
}

func (m *modbusTCPTwin) Subscribe(eventID models.ProductEventID, bus chan<- *models.DeviceDataWrapper) error {
	return errors.NewCommonEdgeError(errors.MethodNotAllowed, fmt.Sprintf("ModbusTCP device does not support events' subscribing"), nil)
}

func (m *modbusTCPTwin) Call(methodID models.ProductMethodID, ins map[models.ProductPropertyID]*models.DeviceData) (outs map[models.ProductPropertyID]*models.DeviceData, err error) {
	return nil, errors.NewCommonEdgeError(errors.MethodNotAllowed, fmt.Sprintf("ModbusTCP device does not support methods' calling"), nil)
}
