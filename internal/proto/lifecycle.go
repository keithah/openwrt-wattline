package proto

import (
	"encoding/binary"
	"fmt"
)

func OTAEnter() []byte { return []byte{'P', 'K'} }
func OTAExit() []byte  { return []byte{0x83} }

// OTAInfo is the reply to the OTA INFO command.
type OTAInfo struct {
	Mode      byte
	OTAStart  uint32
	BlockSize uint16
	ChipType  uint16
	CID       uint16
	AppStart  uint32
	Revision  byte
}

func ParseOTAInfo(reply []byte) (OTAInfo, error) {
	if len(reply) == 0 {
		return OTAInfo{}, fmt.Errorf("empty OTA INFO reply")
	}
	info := OTAInfo{Mode: reply[0]}
	switch info.Mode {
	case 1:
		if len(reply) < 15 {
			return OTAInfo{}, fmt.Errorf("app OTA INFO reply too short: % x", reply)
		}
	case 2:
		if len(reply) < 16 {
			return OTAInfo{}, fmt.Errorf("bootloader OTA INFO reply too short: % x", reply)
		}
		info.OTAStart = binary.LittleEndian.Uint32(reply[1:5])
		info.BlockSize = binary.LittleEndian.Uint16(reply[5:7])
		info.ChipType = binary.LittleEndian.Uint16(reply[7:9])
		info.AppStart = binary.LittleEndian.Uint32(reply[9:13])
		info.Revision = reply[15]
	default:
		return OTAInfo{}, fmt.Errorf("unknown OTA mode %d", info.Mode)
	}
	info.CID = binary.LittleEndian.Uint16(reply[13:15])
	return info, nil
}
