package stream

import (
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/juju/errors"
	"github.com/tiglabs/baudstorage/proto"
	"github.com/tiglabs/baudstorage/sdk/data"
	"github.com/tiglabs/baudstorage/util"
	"github.com/tiglabs/baudstorage/util/log"
	"net"
	"sync/atomic"
)

const (
	MaxSelectDataPartionForWrite = 32
	IsFlushIng                   = 1
	NoFlushIng                   = -1
	MaxStreamInitRetry           = 3
)

type WriteRequest struct {
	data           []byte
	size           int
	canWrite       int
	err            error
	kernelOffset   int
	isFlushRequest bool
}

type StreamWriter struct {
	sync.Mutex
	w                  *data.Wrapper
	currentWriter      *ExtentWriter //current ExtentWriter
	errCount           int           //error count
	currentPartitionId uint32        //current PartitionId
	currentExtentId    uint64        //current FileId
	Inode              uint64        //inode
	excludePartition   []uint32
	appendExtentKey    AppendExtentKeyFunc
	requestCh          chan *WriteRequest
	replyCh            chan *WriteRequest
	exitCh             chan bool
	flushLock          sync.Mutex
	isFlushIng         int32
	hasUpdateKey       map[string]int
	HasWriteSize       uint64
}

func NewStreamWriter(w *data.Wrapper, inode uint64, appendExtentKey AppendExtentKeyFunc) (stream *StreamWriter) {
	stream = new(StreamWriter)
	stream.w = w
	stream.appendExtentKey = appendExtentKey
	stream.Inode = inode
	stream.requestCh = make(chan *WriteRequest, 1000)
	stream.replyCh = make(chan *WriteRequest, 1000)
	stream.exitCh = make(chan bool, 10)
	stream.excludePartition = make([]uint32, 0)
	stream.hasUpdateKey = make(map[string]int)
	go stream.server()

	return
}

//get current extent writer
func (stream *StreamWriter) getWriter() (writer *ExtentWriter) {
	stream.Lock()
	defer stream.Unlock()
	return stream.currentWriter
}

//set current extent Writer to null
func (stream *StreamWriter) setWriterToNull() {
	stream.Lock()
	defer stream.Unlock()
	stream.currentWriter = nil
}

//set writer
func (stream *StreamWriter) setWriter(writer *ExtentWriter) {
	stream.Lock()
	defer stream.Unlock()
	stream.currentWriter = writer
}

func (stream *StreamWriter) toString() (m string) {
	currentWriterMsg := ""
	if stream.getWriter() != nil {
		currentWriterMsg = stream.getWriter().toString()
	}
	return fmt.Sprintf("inode[%v] currentDataPartion[%v] currentExtentId[%v]"+
		" errCount[%v]", stream.Inode, stream.currentPartitionId, currentWriterMsg,
		stream.errCount)
}

func (stream *StreamWriter) toStringWithWriter(writer *ExtentWriter) (m string) {
	currentWriterMsg := writer.toString()
	return fmt.Sprintf("inode[%v] currentDataPartion[%v] currentExtentId[%v]"+
		" errCount[%v]", stream.Inode, stream.currentPartitionId, currentWriterMsg,
		stream.errCount)
}

//stream init,alloc a extent ,select dp and extent
func (stream *StreamWriter) init() (err error) {
	if stream.getWriter() != nil && stream.getWriter().isFullExtent() {
		err = stream.flushCurrExtentWriter()
	}
	if err != nil {
		return errors.Annotatef(err, "WriteInit")
	}
	if stream.getWriter() != nil {
		return
	}
	var writer *ExtentWriter
	writer, err = stream.allocateNewExtentWriter()
	if err != nil {
		err = errors.Annotatef(err, "WriteInit AllocNewExtentFailed")
		return err
	}
	stream.setWriter(writer)
	return
}

func (stream *StreamWriter) server() {
	t := time.NewTicker(time.Second * 2)
	defer t.Stop()
	for {
		if atomic.LoadUint64(&stream.HasWriteSize) >= 3*util.MB {
			stream.flushCurrExtentWriter()
		}
		select {
		case request := <-stream.requestCh:
			if request.isFlushRequest {
				request.err = stream.flushCurrExtentWriter()
			} else {
				request.canWrite, request.err = stream.write(request.data, request.kernelOffset, request.size)
			}
			stream.replyCh <- request
		case <-stream.exitCh:
			return
		case <-t.C:
			if stream.getWriter() == nil {
				continue
			}
			if stream.isFlushIng == IsFlushIng {
				continue
			}
			stream.flushCurrExtentWriter()
		}
	}
}

func (stream *StreamWriter) write(data []byte, offset, size int) (total int, err error) {
	var (
		write int
	)
	defer func() {
		atomic.AddUint64(&stream.HasWriteSize, uint64(total))
		if err == nil {
			return
		}
		err = errors.Annotatef(err, "UserRequest{inode(%v) write "+
			"KernelOffset(%v) KernelSize(%v)} stream{ (%v) occous error}",
			stream.Inode, offset, size, stream.toString())
		log.LogError(err.Error())
		log.LogError(errors.ErrorStack(err))
	}()

	var initRetry int = 0
	for total < size {
		if err = stream.init(); err != nil {
			if initRetry++; initRetry > MaxStreamInitRetry {
				return
			}
			continue
		}
		write, err = stream.getWriter().write(data[total:size], offset, size-total)
		if err == FullExtentErr {
			continue
		}
		if err == nil {
			total += write
			continue
		}
		if err = stream.recoverExtent(); err != nil {
			return
		}
		write = size - total
		total += write
	}

	return
}

func (stream *StreamWriter) close() (err error) {
	if stream.getWriter() != nil {
		stream.Lock()
		err = stream.currentWriter.close()
		stream.Unlock()
	}
	if err == nil {
		stream.exitCh <- true
	}

	return
}

func (stream *StreamWriter) flushCurrExtentWriter() (err error) {
	var status error
	defer func() {
		stream.isFlushIng = NoFlushIng
		stream.flushLock.Unlock()
		if err == nil || status == syscall.ENOENT {
			stream.errCount = 0
			atomic.StoreUint64(&stream.HasWriteSize, 0)
			err = nil
			return
		}
		stream.errCount++
		if stream.errCount < MaxSelectDataPartionForWrite {
			if err = stream.recoverExtent(); err == nil {
				err = stream.flushCurrExtentWriter()
			}
		}
	}()
	stream.isFlushIng = IsFlushIng
	stream.flushLock.Lock()
	writer := stream.getWriter()
	if writer == nil {
		err = nil
		return nil
	}
	if err = writer.flush(); err != nil {
		err = errors.Annotatef(err, "writer[%v] Flush Failed", writer.toString())
		return err
	}
	if err = stream.updateToMetaNode(); err != nil {
		err = errors.Annotatef(err, "update to MetaNode failed[%v]", err.Error())
		return err
	}
	if writer.isFullExtent() {
		writer.close()
		stream.w.PutConnect(writer.getConnect(), NoCloseConnect)
		if err = stream.updateToMetaNode(); err != nil {
			err = errors.Annotatef(err, "update to MetaNode failed[%v]", err.Error())
			return err
		}
		stream.setWriterToNull()
	}

	return err
}

func (stream *StreamWriter) updateToMetaNodeSize() (sumSize int) {
	for _, v := range stream.hasUpdateKey {
		sumSize += v
	}
	return sumSize
}

func (stream *StreamWriter) updateToMetaNode() (err error) {
	for i := 0; i < MaxSelectDataPartionForWrite; i++ {
		ek := stream.getWriter().toKey() //first get currentExtent Key
		key := ek.GetExtentKey()
		lastUpdateExtentKeySize, ok := stream.hasUpdateKey[key]
		if ok && lastUpdateExtentKeySize == int(ek.Size) {
			return nil
		}
		if ek.Size != 0 {
			err = stream.appendExtentKey(stream.Inode, ek) //put it to metanode
			if err == syscall.ENOENT {
				return
			}
			if err == nil {
				stream.hasUpdateKey[key] = int(ek.Size)
				log.LogInfof("inode[%v] update ek[%v] has update filesize[%v] ", stream.Inode, ek.String(), stream.updateToMetaNodeSize())
				return
			} else {
				err = errors.Annotatef(err, "update extent[%v] to MetaNode Failed", ek.Size)
				log.LogErrorf("stream[%v] err[%v]", stream.toString(), err.Error())
				continue
			}
		}
		break
	}
	return err
}

func (stream *StreamWriter) writeRecoverPackets(writer *ExtentWriter, retryPackets []*Packet) (err error) {
	for _, p := range retryPackets {
		log.LogInfof("recover packet [%v] kernelOffset[%v] to extent[%v]",
			p.GetUniqueLogId(), p.kernelOffset, writer.toString())
		_, err = writer.write(p.Data, p.kernelOffset, int(p.Size))
		if err != nil {
			err = errors.Annotatef(err, "pkg[%v] RecoverExtent write failed", p.GetUniqueLogId())
			log.LogErrorf("stream[%v] err[%v]", stream.toStringWithWriter(writer), err.Error())
			stream.excludePartition = append(stream.excludePartition, writer.dp.PartitionID)
			return err
		}
	}
	return
}

func (stream *StreamWriter) recoverExtent() (err error) {
	stream.excludePartition = append(stream.excludePartition, stream.getWriter().dp.PartitionID) //exclude current PartionId
	stream.getWriter().notifyExit()
	retryPackets := stream.getWriter().getNeedRetrySendPackets() //get need retry recover packets
	for i := 0; i < MaxSelectDataPartionForWrite; i++ {
		if err = stream.updateToMetaNode(); err == nil {
			break
		}
	}
	var writer *ExtentWriter
	for i := 0; i < MaxSelectDataPartionForWrite; i++ {
		err = nil
		if writer, err = stream.allocateNewExtentWriter(); err != nil { //allocate new extent
			err = errors.Annotatef(err, "RecoverExtent Failed")
			log.LogErrorf("stream[%v] err[%v]", stream.toString(), err.Error())
			continue
		}
		if err = stream.writeRecoverPackets(writer, retryPackets); err == nil {
			stream.excludePartition = make([]uint32, 0)
			stream.setWriter(writer)
			stream.updateToMetaNode()
			return
		} else {
			writer.forbirdUpdateToMetanode()
			writer.notifyExit()
		}
	}

	return

}

func (stream *StreamWriter) allocateNewExtentWriter() (writer *ExtentWriter, err error) {
	var (
		dp       *data.DataPartition
		extentId uint64
	)
	err = fmt.Errorf("cannot alloct new extent after maxrery")
	for i := 0; i < MaxSelectDataPartionForWrite; i++ {
		if dp, err = stream.w.GetWriteDataPartition(stream.excludePartition); err != nil {
			log.LogWarn(fmt.Sprintf("stream [%v] ActionAllocNewExtentWriter "+
				"failed on getWriteDataPartion,error[%v] execludeDataPartion[%v]", stream.toString(), err.Error(), stream.excludePartition))
			continue
		}
		if extentId, err = stream.createExtent(dp); err != nil {
			log.LogWarn(fmt.Sprintf("stream [%v]ActionAllocNewExtentWriter "+
				"create Extent,error[%v] execludeDataPartion[%v]", stream.toString(), err.Error(), stream.excludePartition))
			continue
		}
		if writer, err = NewExtentWriter(stream.Inode, dp, stream.w, extentId); err != nil {
			log.LogWarn(fmt.Sprintf("stream [%v] ActionAllocNewExtentWriter "+
				"NewExtentWriter[%v],error[%v] execludeDataPartion[%v]", stream.toString(), extentId, err.Error(), stream.excludePartition))
			continue
		}
		break
	}
	if extentId <= 0 {
		log.LogErrorf(errors.Annotatef(err, "allocateNewExtentWriter").Error())
		return nil, errors.Annotatef(err, "allocateNewExtentWriter")
	}
	stream.currentPartitionId = dp.PartitionID
	stream.currentExtentId = extentId
	err = nil

	return writer, nil
}

func (stream *StreamWriter) createExtent(dp *data.DataPartition) (extentId uint64, err error) {
	var (
		connect *net.TCPConn
	)
	conn, err := net.DialTimeout("tcp", dp.Hosts[0], time.Second)
	if err != nil {
		err = errors.Annotatef(err, " get connect from datapartionHosts[%v]", dp.Hosts[0])
		return 0, err
	}
	connect, _ = conn.(*net.TCPConn)
	connect.SetKeepAlive(true)
	connect.SetNoDelay(true)
	defer connect.Close()
	p := NewCreateExtentPacket(dp, stream.Inode)
	if err = p.WriteToConn(connect); err != nil {
		err = errors.Annotatef(err, "send CreateExtent[%v] to datapartionHosts[%v]", p.GetUniqueLogId(), dp.Hosts[0])
		return
	}
	if err = p.ReadFromConn(connect, proto.ReadDeadlineTime*2); err != nil {
		err = errors.Annotatef(err, "receive CreateExtent[%v] failed datapartionHosts[%v]", p.GetUniqueLogId(), dp.Hosts[0])
		return
	}
	if p.ResultCode != proto.OpOk {
		err = errors.Annotatef(err, "receive CreateExtent[%v] failed datapartionHosts[%v] ", p.GetUniqueLogId(), dp.Hosts[0])
		return
	}
	extentId = p.FileID
	if p.FileID <= 0 {
		err = errors.Annotatef(err, "illegal extentId[%v] from [%v] response",
			extentId, dp.Hosts[0])
		return

	}

	return extentId, nil
}
