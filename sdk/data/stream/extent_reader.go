package stream

import (
	"fmt"
	"github.com/juju/errors"
	"github.com/tiglabs/baudstorage/proto"
	"github.com/tiglabs/baudstorage/sdk/data"
	"github.com/tiglabs/baudstorage/util"
	"github.com/tiglabs/baudstorage/util/log"
	"github.com/tiglabs/baudstorage/util/pool"
	"hash/crc32"
	"math/rand"
	"net"
	"sync/atomic"
	"time"
)

const (
	ForceCloseConnect = true
	NoCloseConnect    = false
)

var (
	ReadConnectPool = pool.NewConnPoolWithPara(60, 100, time.Second*20, nil)
)

type ExtentReader struct {
	inode            uint64
	startInodeOffset uint64
	endInodeOffset   uint64
	dp               *data.DataPartition
	key              proto.ExtentKey
	w                *data.Wrapper
	readerIndex      uint32
	readcnt          uint64
}

func NewExtentReader(inode uint64, inInodeOffset int, key proto.ExtentKey,
	w *data.Wrapper) (reader *ExtentReader, err error) {
	reader = new(ExtentReader)
	reader.dp, err = w.GetDataPartition(key.PartitionId)
	if err != nil {
		return
	}
	reader.inode = inode
	reader.key = key
	reader.readcnt = 1
	reader.startInodeOffset = uint64(inInodeOffset)
	reader.endInodeOffset = reader.startInodeOffset + uint64(key.Size)
	reader.w = w
	rand.Seed(time.Now().UnixNano())
	reader.readerIndex = uint32(rand.Intn(int(reader.dp.ReplicaNum)))
	return reader, nil
}

func (reader *ExtentReader) read(data []byte, offset, size, kerneloffset, kernelsize int) (err error) {
	if size <= 0 {
		return
	}
	err = reader.readDataFromDataPartition(offset, size, data, kerneloffset, kernelsize)

	return
}

func (reader *ExtentReader) readDataFromDataPartition(offset, size int, data []byte, kerneloffset, kernelsize int) (err error) {
	if _, err = reader.streamReadDataFromHost(offset, size, data, kerneloffset, kernelsize); err != nil {
		log.LogWarnf(err.Error())
		goto forLoop
	}
	return

forLoop:
	mesg := ""
	for i := 0; i < 3; i++ {
		_, err = reader.streamReadDataFromHost(offset, size, data, kerneloffset, kernelsize)
		if err == nil {
			return
		} else {
			log.LogWarn(err.Error())
			mesg += fmt.Sprintf(" (index[%v] err[%v])", i, err.Error())
		}
	}
	log.LogWarn(mesg)
	err = fmt.Errorf(mesg)

	return
}

func (reader *ExtentReader) streamReadDataFromHost(offset, expectReadSize int, data []byte, kerneloffset, kernelsize int) (actualReadSize int, err error) {
	request := NewStreamReadPacket(&reader.key, offset, expectReadSize)
	var connect *net.TCPConn
	index := atomic.LoadUint32(&reader.readerIndex)
	if index >= uint32(reader.dp.ReplicaNum) {
		index = 0
		atomic.StoreUint32(&reader.readerIndex, 0)
	}
	host := reader.dp.Hosts[index]
	connect, err = ReadConnectPool.Get(host)
	if err != nil {
		atomic.AddUint32(&reader.readerIndex, 1)
		return 0, errors.Annotatef(err, reader.toString()+
			"streamReadDataFromHost dp[%v] cannot get  connect from host[%v] request[%v] ",
			reader.key.PartitionId, host, request.GetUniqueLogId())

	}
	defer func() {
		if err != nil {
			atomic.AddUint32(&reader.readerIndex, 1)
			ReadConnectPool.Put(connect, ForceCloseConnect)
		} else {
			ReadConnectPool.Put(connect, NoCloseConnect)
		}
	}()

	if err = request.WriteToConn(connect); err != nil {
		err = errors.Annotatef(err, reader.toString()+"streamReadDataFromHost host[%v] error request[%v]",
			host, request.GetUniqueLogId())
		return 0, err
	}

	for {
		if actualReadSize >= expectReadSize {
			break
		}
		reply := NewReply(request.ReqID, reader.dp.PartitionID, request.FileID)
		canRead := util.Min(util.ReadBlockSize, expectReadSize-actualReadSize)
		reply.Data = data[actualReadSize : canRead+actualReadSize]
		err = reply.ReadFromConnStream(connect, proto.ReadDeadlineTime)
		if err != nil {
			err = errors.Annotatef(err, reader.toString()+"streamReadDataFromHost host[%v]  error reqeust[%v]",
				host, request.GetUniqueLogId())
			return 0, err
		}
		err = reader.checkStreamReply(request, reply, kerneloffset, kernelsize)
		if err != nil {
			return 0, err
		}
		actualReadSize += int(reply.Size)
		if actualReadSize >= expectReadSize {
			break
		}
	}

	return actualReadSize, nil
}

func (reader *ExtentReader) checkStreamReply(request *Packet, reply *Packet, kerneloffset, kernelsize int) (err error) {
	if reply.ResultCode != proto.OpOk {
		return errors.Annotatef(fmt.Errorf("reply status code[%v] is not ok,request [%v] "+
			"but reply [%v] ", reply.ResultCode, request.GetUniqueLogId(), reply.GetUniqueLogId()),
			fmt.Sprintf("reader[%v]", reader.toString()))
	}
	if !request.IsEqualStreamReadReply(reply) {
		return errors.Annotatef(fmt.Errorf("request not equare reply , request [%v] "+
			"and reply [%v] ", request.GetUniqueLogId(), reply.GetUniqueLogId()),
			fmt.Sprintf("reader[%v]", reader.toString()))
	}
	expectCrc := crc32.ChecksumIEEE(reply.Data[:reply.Size])
	if reply.Crc != expectCrc {
		return errors.Annotatef(fmt.Errorf("crc not match on  request [%v] "+
			"and reply [%v] expectCrc[%v] but reciveCrc[%v] ", request.GetUniqueLogId(), reply.GetUniqueLogId(), expectCrc, reply.Crc),
			fmt.Sprintf("reader[%v]", reader.toString()))
	}
	return nil
}

func (reader *ExtentReader) readDataFromHost(offset, expectReadSize int, data []byte, kerneloffset, kernelsize int) (actualReadSize int, err error) {
	request := NewReadPacket(&reader.key, offset, expectReadSize)
	var connect *net.TCPConn
	index := atomic.LoadUint32(&reader.readerIndex)
	if index >= uint32(reader.dp.ReplicaNum) {
		index = 0
		atomic.StoreUint32(&reader.readerIndex, 0)
	}
	host := reader.dp.Hosts[index]
	connect, err = reader.w.GetConnect(host)
	if err != nil {
		atomic.AddUint32(&reader.readerIndex, 1)
		return 0, errors.Annotatef(err, reader.toString()+
			"readDataFromHost dp[%v] cannot get  connect from host[%v] request[%v] ",
			reader.key.PartitionId, host, request.GetUniqueLogId())

	}
	defer func() {
		if err != nil {
			atomic.AddUint32(&reader.readerIndex, 1)
			reader.w.PutConnect(connect, ForceCloseConnect)
		} else {
			reader.w.PutConnect(connect, NoCloseConnect)
		}
	}()

	if err = request.WriteToConn(connect); err != nil {
		err = errors.Annotatef(err, reader.toString()+"readDataFromHost host[%v] error request[%v]",
			host, request.GetUniqueLogId())
		return 0, err
	}
	reply := NewReply(request.ReqID, reader.dp.PartitionID, request.FileID)
	reply.Data = data[:expectReadSize]
	err = reply.ReadFromConnStream(connect, proto.StreamReadDeadLineTime)
	if err != nil {
		err = errors.Annotatef(err, reader.toString()+"readDataFromHost host[%v]  error reqeust[%v]",
			host, request.GetUniqueLogId())
		return 0, err
	}
	err = reader.checkReply(request, reply, kerneloffset, kernelsize)
	if err != nil {
		return 0, err
	}

	return int(request.Size), nil
}

func (reader *ExtentReader) checkReply(request *Packet, reply *Packet, kerneloffset, kernelsize int) (err error) {
	if reply.ResultCode != proto.OpOk {
		return errors.Annotatef(fmt.Errorf("reply status code[%v] is not ok,request [%v] "+
			"but reply [%v] ", reply.ResultCode, request.GetUniqueLogId(), reply.GetUniqueLogId()),
			fmt.Sprintf("reader[%v]", reader.toString()))
	}
	if !request.IsEqualReadReply(reply) {
		return errors.Annotatef(fmt.Errorf("request not equare reply , request [%v] "+
			"and reply [%v] ", request.GetUniqueLogId(), reply.GetUniqueLogId()),
			fmt.Sprintf("reader[%v]", reader.toString()))
	}
	expectCrc := crc32.ChecksumIEEE(reply.Data[:request.Size])
	if reply.Crc != expectCrc {
		return errors.Annotatef(fmt.Errorf("crc not match on  request [%v] "+
			"and reply [%v] expectCrc[%v] but reciveCrc[%v] ", request.GetUniqueLogId(), reply.GetUniqueLogId(), expectCrc, reply.Crc),
			fmt.Sprintf("reader[%v]", reader.toString()))
	}
	return nil
}

func (reader *ExtentReader) updateKey(key proto.ExtentKey) (update bool) {
	if !(key.PartitionId == reader.key.PartitionId && key.ExtentId == reader.key.ExtentId) {
		return
	}
	if key.Size <= reader.key.Size {
		return
	}
	reader.key = key
	end := atomic.LoadUint64(&reader.startInodeOffset) + uint64(key.Size)
	atomic.StoreUint64(&reader.endInodeOffset, end)

	return true
}

func (reader *ExtentReader) toString() (m string) {
	return fmt.Sprintf("inode [%v] extentKey[%v] start[%v] end[%v]", reader.inode,
		reader.key.Marshal(), reader.startInodeOffset, reader.endInodeOffset)
}
