package driver

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/thingio/edge-device-std/errors"
	"github.com/thingio/edge-device-std/models"
	"math"
)

const (
	ModbusFuncReadCoils          = 0x01
	ModbusFuncReadDiscreteInputs = 0x02
	ModbusFuncReadHoldingRgs     = 0x03
	ModbusFuncReadInputRgs       = 0x04
	ModbusFuncWriteMultiCoils    = 0x0F
	ModbusFuncWriteMultiRgs      = 0x10

	ModbusPDUMaxLength        = 253 // 数据部分最大长度(数据来源:GBT 19582.1-2008)
	ModbusMBAPLength          = 7
	ModbusSlaveAddrLength     = 1
	ModbusCRCLength           = 2
	ModbusRspPDUFuncCodeIndex = 0
	ModbusRspPDUByteNumIndex  = 1
	ModbusRspPDUPrefixLength  = 2

	ModbusRgsTypeCoil                = "coil"           // 线圈 读写
	ModbusRgsTypeDiscreteInputStatus = "input_status"   // 离散输入状态 只读
	ModbusRgsTypeHoldingRegister     = "hold_register"  // 保持寄存器 读写
	ModbusRgsTypeInputRegister       = "input_register" // 输入寄存器 只读

	ModbusExtendRgsType  = "rgs_type"  // 数据类型,主要分为离散量输入(1bit,只读)、线圈状态(1bit,读写)、输入寄存器(16bits,只读)、保持寄存器(16bits,读写)
	ModbusExtendRgsAddr  = "rgs_addr"  // 线圈/离散输入/寄存器的起始地址,0x0至0xFFFF
	ModbusExtendRgsCount = "rgs_count" // 读取/写入的线圈/离散输入/寄存器的数目

	ModbusTCPDeviceIP           = "ip"            // ModbusTcp设备/服务器所在IP
	ModbusTCPDevicePort         = "port"          // ModbusTcp设备/服务器服务的端口,默认为502
	ModbusTCPDeviceTimeout      = "timeout"       // ModbusTcp设备访问超时间隔
	ModbusTCPDeviceSlave        = "slave"         // 若ModbusTcp设备为通过网关接入的串行设备,则该字段为其从设备号,否则无效
	ModbusTCPDeviceSerialBridge = "serial_bridge" // 设备是否为通过网关接入的串行设备

	ModbusRTUDeviceSlave         = "slave"         // ModbusRTU设备的从站号
	ModbusRTUDeviceBaudRate      = "baud_rate"     // ModbusRTU设备波特率
	ModbusRTUDeviceTimeout       = "timeout"       // ModbusRTU设备访问超时间隔
	ModbusRTUDeviceSerialDevice  = "serial_device" // ModbusRTU串行设备地址
	ModbusRTUDeviceStartAddr     = "start_addr"
	ModbusRTUDeviceNumBytes      = "num_bytes"
	ModbusRTUDeviceResponsePause = "response_pause"
)

var (
	ReadFuncCodes = map[string]byte{
		ModbusRgsTypeCoil:                ModbusFuncReadCoils,
		ModbusRgsTypeInputRegister:       ModbusFuncReadInputRgs,
		ModbusRgsTypeHoldingRegister:     ModbusFuncReadHoldingRgs,
		ModbusRgsTypeDiscreteInputStatus: ModbusFuncReadDiscreteInputs,
	}
	WriteFuncCodes = map[string]byte{
		ModbusRgsTypeHoldingRegister: ModbusFuncWriteMultiRgs,
		ModbusRgsTypeCoil:            ModbusFuncWriteMultiCoils,
	}
)

func getDataHead(rgsAddr, rgsNum uint16) []byte {
	bs := make([]byte, 4)
	binary.BigEndian.PutUint16(bs[:2], rgsAddr)
	binary.BigEndian.PutUint16(bs[2:], rgsNum)
	return bs
}
func pud2Data(dataType string, order binary.ByteOrder, bs []byte) (interface{}, error) {
	if len(bs) < 2 || uint8(len(bs)-ModbusRspPDUPrefixLength) != bs[ModbusRspPDUByteNumIndex] {
		return nil, errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("error while converting pud to data, invalid pdu [%x]", bs), nil)
	}
	bn := bs[ModbusRspPDUByteNumIndex] // byte count
	data := bs[ModbusRspPDUPrefixLength:]
	switch dataType {
	case models.PropertyValueTypeBool:
		if bn != 1 {
			return nil, errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("%d bytes cannot parsed to data type bool", bn), nil)
		}
		switch data[0] {
		case 0x00:
			return false, nil
		case 0x01:
			return true, nil
		default:
			return nil, errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("%d bytes cannot parsed to data type bool", bn), nil)
		}

	case models.PropertyValueTypeFloat:
		switch bn {
		case 4:
			return math.Float32frombits(order.Uint32(data)), nil
		case 8:
			return math.Float64frombits(order.Uint64(data)), nil
		default:
			return nil, errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("%d bytes cannot parsed to data type float32/float64", bn), nil)
		}
	case models.PropertyValueTypeInt:
		if bn > 8 {
			return nil, errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("%d bytes cannot parsed to data type int64", bn), nil)
		}
		zs := make([]byte, 8-bn) // zeros
		return int64(order.Uint64(append(zs, data...))), nil
	case models.PropertyValueTypeUint:
		if bn > 8 {
			return nil, errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("%d bytes cannot parsed to data type int64", bn), nil)
		}
		zs := make([]byte, 8-bn) // zeros
		return order.Uint64(append(zs, data...)), nil
	case models.PropertyValueTypeString:
		return string(data), nil
	}
	return nil, errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("device data type : %s not supported yet", dataType), nil)
}
func data2Bytes(dataType string, order binary.ByteOrder, data interface{}, size int) ([]byte, error) {
	bs := make([]byte, 0)
	b := bytes.NewBuffer(bs)
	switch dataType {
	case models.PropertyValueTypeBool:
		if size != 1 {
			return bs, errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("device data type : bool cannot be writen to %d bytes", size), nil)
		}
		if data.(bool) {
			bs[0] = uint8(1)
		} else {
			bs[0] = uint8(0)
		}
	case models.PropertyValueTypeFloat:
		switch size {
		case 4:
			order.PutUint32(bs, math.Float32bits(data.(float32)))
			return bs, nil
		case 8:
			order.PutUint64(bs, math.Float64bits(data.(float64)))
			return bs, nil
		default:
			return bs, errors.NewCommonEdgeError(errors.Driver,
				fmt.Sprintf("float32/float64 cannot be writen into %d bytes", size), nil)
		}
	case models.PropertyValueTypeInt, models.PropertyValueTypeUint:
		if err := binary.Write(b, order, data); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	case models.PropertyValueTypeString:
		return []byte(data.(string)), nil
	}
	return nil, errors.NewCommonEdgeError(errors.Driver,
		fmt.Sprintf("device data type : %s not supported yet", dataType), nil)
}
