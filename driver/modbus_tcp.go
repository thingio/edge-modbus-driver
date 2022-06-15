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
	tsID   int      // transaction id
	pid    string   // product id
	did    string   // device id
	conn   net.Conn // tcp connection
	closed bool
	stop   chan bool
	lock   sync.Mutex
	once   sync.Once // ensure there is only one reconnect goroutine
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
	events     map[models.ProductEventID]*models.ProductEvent       // for event's subscribing
	methods    map[models.ProductMethodID]*models.ProductMethod     // for method's calling

	lg *logger.Logger

	ctx    context.Context
	cancel context.CancelFunc
}

func newModbusTCPDriver(device *models.Device) *modbusTCPDriver {
	conf, err := parseModbusTcpDeviceConf(device)
	if err != nil {
		return nil
	}
	return &modbusTCPDriver{
		modbusTCPDeviceConf: conf,
		tsID:                0,
		pid:                 "",
		did:                 "",
		conn:                nil,
		stop:                make(chan bool),
		closed:              false,
		lock:                sync.Mutex{},
		once:                sync.Once{},
	}
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
			dc.slave = []byte(v)[0]
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
	tcpDriver := newModbusTCPDriver(device)
	if tcpDriver == nil {
		return nil, errors.NewCommonEdgeError(errors.DeviceTwin, "tcpDriver is nil", nil)
	}
	twin := &modbusTCPTwin{
		modbusTCPDriver: tcpDriver,
		product:         product,
		device:          device,

		properties: make(map[models.ProductPropertyID]*models.ProductProperty),
		events:     make(map[models.ProductEventID]*models.ProductEvent),
		methods:    make(map[models.ProductMethodID]*models.ProductMethod),
	}
	return twin, nil
}

func (m *modbusTCPTwin) Initialize(lg *logger.Logger) error {
	m.lg = lg

	m.modbusTCPDriver.pid = m.product.ID
	m.modbusTCPDriver.did = m.device.ID
	m.properties = make(map[models.ProductPropertyID]*models.ProductProperty)
	for _, property := range m.product.Properties {
		m.properties[property.Id] = property
	}
	for _, event := range m.product.Events {
		m.events[event.Id] = event
	}
	return nil
}

func (m *modbusTCPTwin) Start(ctx context.Context) error {
	m.ctx, m.cancel = context.WithCancel(ctx)

	if m.closed == true {
		m.lg.Error("modbusTwin.Start Error: obj has been stopped")
		return nil
	}
	addr, err := net.ResolveTCPAddr("tcp4", fmt.Sprintf("%s:%d", m.ip, m.port))
	if err != nil {
		m.lg.Errorf("modbusTwin.Start Error: illegal addr: %+v, err: %+v", addr, err)
		return err
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	conn, err := net.DialTimeout("tcp", addr.String(), time.Duration(m.timeoutMS))
	if err != nil {
		m.lg.Errorf("modbusTwin.Start Error: TCP Connection addr: %+v, err: %+v", addr, err)
		return err
	}
	m.conn = conn
	return nil
}

func (m *modbusTCPTwin) Stop(force bool) error {
	m.cancel()

	m.modbusTCPDriver.closed = true
	m.modbusTCPDriver.stop <- true
	if m.conn != nil {
		if err := m.conn.Close(); err != nil {
			m.lg.Info("Stop ModbusTwin TCPDriver Error: %s:%s, err: %+v", m.ip, m.port, err)
			return err
		}
	}
	return nil
}

func (m *modbusTCPTwin) HealthCheck() (*models.DeviceStatus, error) {
	if m.closed {
		return &models.DeviceStatus{
			Device:      m.device,
			State:       models.DeviceStateDisconnected,
			StateDetail: "连接已关闭",
		}, nil
	}
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
	res := make(map[models.ProductPropertyID]*models.DeviceData)
	property, ok := m.properties[propertyID]
	if !ok {
		return nil, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("the property[%s] hasn't been ready", property.Id), nil)
	}
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
	m.lock.Unlock()
	if err != nil {
		m.lg.Errorf("modbus HardRead Error: propertyID %+v, error %+v", propertyID, err)
		return res, errors.NewCommonEdgeError(errors.BadRequest, "read modbus TCP device failed", nil)
	}
	value, err := pud2Data(property.FieldType, binary.BigEndian, m.pdu(resp))
	if err != nil {
		return res, err
	}
	res[propertyID] = value.(*models.DeviceData)
	return res, nil
}
func (d *modbusTCPDriver) pdu(frame []byte) []byte {
	return frame[ModbusMBAPLength:]
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
	value := values[propertyID]
	bs, err := data2Bytes(property.FieldType, binary.BigEndian, value, int(follow))
	if err != nil {
		return err
	}

	m.lock.Lock()
	_, err = modbusclient.TCPWrite(
		m.conn,
		m.timeoutMS,
		m.tsID,
		funcCode,
		m.serialBridge,
		m.slave,
		append(head, bs...),
		false)
	m.lock.Unlock()
	if err != nil {
		return errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("%+v write modbus tcp device failed", err), nil)
	}
	return nil
}

func (m *modbusTCPTwin) Subscribe(eventID models.ProductEventID, bus chan<- *models.DeviceDataWrapper) error {
	return errors.NewCommonEdgeError(errors.MethodNotAllowed, fmt.Sprintf("ModbusTCP device does not support call-method"), nil)
}

func (m *modbusTCPTwin) Call(methodID models.ProductMethodID, ins map[models.ProductPropertyID]*models.DeviceData) (outs map[models.ProductPropertyID]*models.DeviceData, err error) {
	return nil, errors.NewCommonEdgeError(errors.MethodNotAllowed, fmt.Sprintf("ModbusTCP device does not support call-method"), nil)
}
