package meta

import (
	"fmt"
	"io"
	"net"
	"time"

	"github.com/juju/errors"

	"github.com/tiglabs/baudstorage/proto"
	"github.com/tiglabs/baudstorage/util/log"
)

const (
	SendRetryLimit    = 100
	SendRetryInterval = 100 * time.Millisecond
	SendTimeLimit     = 20 * time.Second
)

type MetaConn struct {
	conn *net.TCPConn
	id   uint64 //PartitionID
	addr string //MetaNode addr
}

// Connection managements
//

func (mc *MetaConn) String() string {
	return fmt.Sprintf("partitionID(%v) addr(%v)", mc.id, mc.addr)
}

func (mw *MetaWrapper) getConn(partitionID uint64, addr string) (*MetaConn, error) {
	conn, err := mw.conns.Get(addr)
	if err != nil {
		log.LogWarnf("Get conn: addr(%v) err(%v)", addr, err)
		return nil, err
	}
	mc := &MetaConn{conn: conn, id: partitionID, addr: addr}
	log.LogDebugf("Get connection: mc(%v)", mc)
	return mc, nil
}

func (mw *MetaWrapper) putConn(mc *MetaConn, err error) {
	if err != nil {
		mw.conns.Put(mc.conn, true)
	} else {
		mw.conns.Put(mc.conn, false)
	}
}

func (mw *MetaWrapper) sendToMetaPartition(mp *MetaPartition, req *proto.Packet) (*proto.Packet, error) {
	var (
		resp  *proto.Packet
		err   error
		addr  string
		mc    *MetaConn
		start time.Time
		op    string
	)

	op = req.GetOpMsg()
	addr = mp.LeaderAddr
	if addr == "" {
		goto retry
	}
	mc, err = mw.getConn(mp.PartitionID, addr)
	if err != nil {
		goto retry
	}
	resp, err = mc.send(req)
	mw.putConn(mc, err)
	if err == nil && !resp.ShallRetry() {
		goto out
	}
	log.LogWarnf("sendToMetaPartition: leader failed mp(%v) mc(%v) err(%v) op(%v) result(%v)", mp, mc, err, op, resp.GetResultMesg())

retry:
	start = time.Now()
	for i := 0; i < SendRetryLimit; i++ {
		for _, addr = range mp.Members {
			mc, err = mw.getConn(mp.PartitionID, addr)
			if err != nil {
				continue
			}
			resp, err = mc.send(req)
			mw.putConn(mc, err)
			if err == nil && !resp.ShallRetry() {
				goto out
			}
			log.LogWarnf("sendToMetaPartition: retry failed mp(%v) mc(%v) err(%v) op(%v) result(%v)", mp, mc, err, op, resp.GetResultMesg())
		}
		if time.Since(start) > SendTimeLimit {
			log.LogWarnf("sendToMetaPartition: retry timeout mp(%v) op(%v) time(%v)", mp, op, time.Since(start))
			break
		}
		log.LogWarnf("sendToMetaPartition: mp(%v) op(%v) retry in (%v)", mp, op, SendRetryInterval)
		time.Sleep(SendRetryInterval)
	}

out:
	if err != nil {
		return nil, errors.New(fmt.Sprintf("sendToMetaPartition faild: mp(%v) op(%v)", mp, req.GetOpMsg()))
	}
	log.LogDebugf("sendToMetaPartition successful: mc(%v) op(%v) result(%v)", mc, req.GetOpMsg(), resp.GetResultMesg())
	return resp, nil
}

func (mc *MetaConn) send(req *proto.Packet) (resp *proto.Packet, err error) {
	err = req.WriteToConn(mc.conn)
	if err != nil {
		return nil, errors.Annotatef(err, "Failed to write to conn")
	}
	resp = proto.NewPacket()
	err = resp.ReadFromConn(mc.conn, proto.ReadDeadlineTime)
	if err != nil && err != io.EOF {
		return nil, errors.Annotatef(err, "Failed to read from conn")
	}
	return resp, nil
}
