package core

import (
	"encoding/binary"
	"io"

	"github.com/google/uuid"
)

// encodeVarint - Вспомогательная функция для olcrtc-совместимого протокола
func encodeVarint(value uint64) []byte {
	buf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(buf, value)
	return buf[:n]
}

// encodeField - Кодирует поле в формате SaluteJazz (Proto-like)
func encodeField(fieldNumber int, wireType int, data []byte) []byte {
	tag := encodeVarint(uint64(fieldNumber)<<3 | uint64(wireType))
	switch wireType {
	case 2:
		length := encodeVarint(uint64(len(data)))
		result := make([]byte, 0, len(tag)+len(length)+len(data))
		result = append(result, tag...)
		result = append(result, length...)
		result = append(result, data...)
		return result
	default:
		result := make([]byte, 0, len(tag)+len(data))
		result = append(result, tag...)
		result = append(result, data...)
		return result
	}
}

// EncodeDataPacket инкапсулирует полезную нагрузку в формат данных SaluteJazz.
// Используется для передачи данных внутри видео-трека.
func EncodeDataPacket(payload []byte) []byte {
	msgID := uuid.New().String()

	userFields := encodeField(2, 2, payload)
	userFields = append(userFields, encodeField(8, 2, []byte(msgID))...)

	dp := encodeField(1, 0, encodeVarint(0))
	dp = append(dp, encodeField(2, 2, userFields)...)

	return dp
}

// DecodeDataPacket извлекает полезную нагрузку из пакета SaluteJazz.
func DecodeDataPacket(raw []byte) ([]byte, bool) {
	userData, ok := parseProtoFields(raw, 2)
	if !ok {
		return nil, false
	}

	payload, ok := parseProtoFields(userData, 2)
	return payload, ok
}

func parseProtoFields(data []byte, targetField int) ([]byte, bool) {
	reader := &protoByteReader{data: data, pos: 0}
	var result []byte

	for reader.pos < len(reader.data) {
		tagVal, err := binary.ReadUvarint(reader)
		if err != nil {
			break
		}

		fieldNumber := int(tagVal >> 3)
		wireType := int(tagVal & 0x07)

		fieldData, ok := handleProtoWireType(reader, wireType, len(data))
		if !ok {
			return result, len(result) > 0
		}

		if fieldNumber == targetField && wireType == 2 {
			result = fieldData
		}
	}

	return result, len(result) > 0
}

func handleProtoWireType(reader *protoByteReader, wireType int, dataLen int) ([]byte, bool) {
	switch wireType {
	case 0:
		_, _ = binary.ReadUvarint(reader)
		return nil, true
	case 2:
		length, err := binary.ReadUvarint(reader)
		if err != nil {
			return nil, false
		}
		if length > uint64(dataLen)-uint64(reader.pos) {
			return nil, false
		}
		fieldData := make([]byte, length)
		n, err := reader.Read(fieldData)
		if err != nil || uint64(n) != length {
			return nil, false
		}
		return fieldData, true
	case 1:
		reader.pos += 8
		return nil, true
	case 5:
		reader.pos += 4
		return nil, true
	default:
		return nil, false
	}
}

type protoByteReader struct {
	data []byte
	pos  int
}

func (b *protoByteReader) ReadByte() (byte, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	c := b.data[b.pos]
	b.pos++
	return c, nil
}

func (b *protoByteReader) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}
