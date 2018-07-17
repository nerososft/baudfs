package proto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
)

type StreamKey struct {
	Inode   uint64
	Extents []ExtentKey
	sync.Mutex
}

func (sk *StreamKey) String() string {
	buff := bytes.NewBuffer(make([]byte, 0))
	buff.WriteString("Inode{")
	buff.WriteString(fmt.Sprintf("Inode[%d]", sk.Inode))
	buff.WriteString(fmt.Sprintf("Extents[%v]", sk.Extents))
	buff.WriteString("}")
	return buff.String()
}

func NewStreamKey(ino uint64) *StreamKey {
	return &StreamKey{
		Inode: ino,
	}
}

func (sk *StreamKey) Marshal() (data []byte, err error) {
	return json.Marshal(sk)
}

func (sk *StreamKey) ToString() (m string) {
	data, _ := json.Marshal(sk)
	return string(data)
}

func (sk *StreamKey) UnMarshal(data []byte) {
	json.Unmarshal(data, sk)
}

func (sk *StreamKey) Put(k ExtentKey) {
	sk.Lock()
	defer sk.Unlock()
	if len(sk.Extents) == 0 {
		sk.Extents = append(sk.Extents, k)
		return
	}
	lastIndex := len(sk.Extents) - 1
	lastKey := sk.Extents[lastIndex]
	if lastKey.PartitionId == k.PartitionId && lastKey.ExtentId == k.ExtentId {
		if k.Size > lastKey.Size {
			sk.Extents[lastIndex].Size = k.Size
			return
		}
		return
	}
	extentsLen := len(sk.Extents)
	for i := 0; i < extentsLen; i++ {
		ek := sk.Extents[i]
		if ek.PartitionId == k.PartitionId && ek.ExtentId == k.ExtentId {
			if k.Size > ek.Size {
				sk.Extents[i].Size = k.Size
				return
			}
			return
		}
	}
	sk.Extents = append(sk.Extents, k)

	return
}

func (sk *StreamKey) Size() (bytes uint64) {
	sk.Lock()
	defer sk.Unlock()
	for _, ok := range sk.Extents {
		bytes += uint64(ok.Size)
	}
	return
}

func (sk *StreamKey) GetExtentLen() int {
	sk.Lock()
	defer sk.Unlock()
	return len(sk.Extents)
}

// Range calls f sequentially for each key and value present in the extent key collection.
// If f returns false, range stops the iteration.
func (sk *StreamKey) Range(f func(i int, v ExtentKey) bool) {
	sk.Lock()
	defer sk.Unlock()
	for i, v := range sk.Extents {
		if !f(i, v) {
			return
		}
	}
}

func (sk *StreamKey) MarshalBinary() (data []byte, err error) {
	sk.Lock()
	defer sk.Unlock()
	buf := bytes.NewBuffer(make([]byte, 0))
	for _, extent := range sk.Extents {
		var binData []byte
		binData, err = extent.MarshalBinary()
		if err != nil {
			return
		}
		buf.Write(binData)
	}
	data = buf.Bytes()
	return
}

func (sk *StreamKey) UnmarshalBinary(data []byte) (err error) {
	sk.Lock()
	defer sk.Unlock()
	buf := bytes.NewBuffer(data)
	for {
		if buf.Len() == 0 {
			break
		}
		var ext ExtentKey
		if err = ext.UnmarshalBinary(buf); err != nil {
			return
		}
		sk.Extents = append(sk.Extents, ext)
	}
	return
}
