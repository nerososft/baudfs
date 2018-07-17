package metanode

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/tiglabs/baudstorage/proto"
	"github.com/tiglabs/baudstorage/util/btree"
)

// Inode wraps necessary properties of `Inode` information in file system.
// Marshal key:
//  +-------+-------+
//  | item  | Inode |
//  +-------+-------+
//  | bytes |   8   |
//  +-------+-------+
// Marshal value:
//  +-------+------+------+-----+----+----+----+--------+------------------+
//  | item  | Type | Size | Gen | CT | AT | MT | ExtLen | MarshaledExtents |
//  +-------+------+------+-----+----+----+----+--------+------------------+
//  | bytes |  4   |  8   |  8  | 8  | 8  | 8  |   4    |      ExtLen      |
//  +-------+------+------+-----+----+----+----+--------+------------------+
// Marshal entity:
//  +-------+-----------+--------------+-----------+--------------+
//  | item  | KeyLength | MarshaledKey | ValLength | MarshaledVal |
//  +-------+-----------+--------------+-----------+--------------+
//  | bytes |     4     |   KeyLength  |     4     |   ValLength  |
//  +-------+-----------+--------------+-----------+--------------+
type Inode struct {
	Inode      uint64 // Inode ID
	Type       uint32
	Size       uint64
	Generation uint64
	CreateTime int64
	AccessTime int64
	ModifyTime int64
	Extents    *proto.StreamKey
}

func (i *Inode) String() string {
	buff := bytes.NewBuffer(make([]byte, 0))
	buff.WriteString("Inode{")
	buff.WriteString(fmt.Sprintf("Inode[%d]", i.Inode))
	buff.WriteString(fmt.Sprintf("Type[%d]", i.Type))
	buff.WriteString(fmt.Sprintf("Size[%d]", i.Size))
	buff.WriteString(fmt.Sprintf("Gen[%d]", i.Generation))
	buff.WriteString(fmt.Sprintf("CT[%d]", i.CreateTime))
	buff.WriteString(fmt.Sprintf("AT[%d]", i.AccessTime))
	buff.WriteString(fmt.Sprintf("MT[%d]", i.ModifyTime))
	buff.WriteString(fmt.Sprintf("Extents[%s]", i.Extents))
	buff.WriteString("}")
	return buff.String()
}

// NewInode returns a new Inode instance pointer with specified Inode ID, name and Inode type code.
// The AccessTime and ModifyTime of new instance will be set to current time.
func NewInode(ino uint64, t uint32) *Inode {
	ts := time.Now().Unix()
	return &Inode{
		Inode:      ino,
		Type:       t,
		Generation: 1,
		CreateTime: ts,
		AccessTime: ts,
		ModifyTime: ts,
		Extents:    proto.NewStreamKey(ino),
	}
}

// Less tests whether the current Inode item is less than the given one.
// This method is necessary fot B-Tree item implementation.
func (i *Inode) Less(than btree.Item) bool {
	ino, ok := than.(*Inode)
	return ok && i.Inode < ino.Inode
}

func (i *Inode) Marshal() (result []byte, err error) {
	keyBytes := i.MarshalKey()
	valBytes := i.MarshalValue()
	keyLen := uint32(len(keyBytes))
	valLen := uint32(len(valBytes))
	buff := bytes.NewBuffer(make([]byte, 0))
	buff.Grow(128)
	if err = binary.Write(buff, binary.BigEndian, keyLen); err != nil {
		return
	}
	if _, err = buff.Write(keyBytes); err != nil {
		return
	}
	if err = binary.Write(buff, binary.BigEndian, valLen); err != nil {

	}
	if _, err = buff.Write(valBytes); err != nil {
		return
	}
	result = buff.Bytes()
	return
}

func (i *Inode) Unmarshal(raw []byte) (err error) {
	var (
		keyLen uint32
		valLen uint32
	)
	buff := bytes.NewBuffer(raw)
	if err = binary.Read(buff, binary.BigEndian, &keyLen); err != nil {
		return
	}
	keyBytes := make([]byte, keyLen)
	if _, err = buff.Read(keyBytes); err != nil {
		return
	}
	if err = i.UnmarshalKey(keyBytes); err != nil {
		return
	}
	if err = binary.Read(buff, binary.BigEndian, &valLen); err != nil {
		return
	}
	valBytes := make([]byte, valLen)
	if _, err = buff.Read(valBytes); err != nil {
		return
	}
	err = i.UnmarshalValue(valBytes)
	return
}

// MarshalKey marshal key to bytes.
func (i *Inode) MarshalKey() (k []byte) {
	k = make([]byte, 8)
	binary.BigEndian.PutUint64(k, i.Inode)
	return
}

// UnmarshalKey unmarshal key from bytes.
func (i *Inode) UnmarshalKey(k []byte) (err error) {
	i.Inode = binary.BigEndian.Uint64(k)
	return
}

// MarshalValue marshal value to bytes.
func (i *Inode) MarshalValue() (val []byte) {
	var err error
	buff := bytes.NewBuffer(make([]byte, 0))
	buff.Grow(64)
	if err = binary.Write(buff, binary.BigEndian, &i.Type); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &i.Size); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &i.Generation); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &i.CreateTime); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &i.AccessTime); err != nil {
		panic(err)
	}
	if err = binary.Write(buff, binary.BigEndian, &i.ModifyTime); err != nil {
		panic(err)
	}
	if i.Extents.Size() != 0 {
		// Marshal ExtentsKey
		extData, err := i.Extents.MarshalBinary()
		if err != nil {
			panic(err)
		}
		if _, err = buff.Write(extData); err != nil {
			panic(err)
		}
	}

	val = buff.Bytes()
	return
}

// UnmarshalValue unmarshal value from bytes.
func (i *Inode) UnmarshalValue(val []byte) (err error) {
	buff := bytes.NewBuffer(val)
	if err = binary.Read(buff, binary.BigEndian, &i.Type); err != nil {
		return
	}
	if err = binary.Read(buff, binary.BigEndian, &i.Size); err != nil {
		return
	}
	if err = binary.Read(buff, binary.BigEndian, &i.Generation); err != nil {
		return
	}
	if err = binary.Read(buff, binary.BigEndian, &i.CreateTime); err != nil {
		return
	}
	if err = binary.Read(buff, binary.BigEndian, &i.AccessTime); err != nil {
		return
	}
	if err = binary.Read(buff, binary.BigEndian, &i.ModifyTime); err != nil {
		return
	}
	if i.Extents == nil {
		i.Extents = proto.NewStreamKey(i.Inode)
	} else {
		i.Extents.Inode = i.Inode
	}
	if buff.Len() == 0 {
		return
	}
	// Unmarshal ExtentsKey
	if err = i.Extents.UnmarshalBinary(buff.Bytes()); err != nil {
		return
	}
	return
}

func (i *Inode) AppendExtents(ext proto.ExtentKey) {
	i.Extents.Put(ext)
	i.Size = i.Extents.Size()
	i.ModifyTime = time.Now().Unix()
}
