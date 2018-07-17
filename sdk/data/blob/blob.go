package blob

import (
	"strings"
	"syscall"
	"time"

	"github.com/juju/errors"
	"github.com/tiglabs/baudstorage/proto"
	"github.com/tiglabs/baudstorage/util"
	"github.com/tiglabs/baudstorage/util/log"
	"github.com/tiglabs/baudstorage/util/pool"
)

const (
	WriteRetryLimit   = 3
	ReadRetryLimit    = 10
	SendRetryLimit    = 10
	SendRetryInterval = 100 * time.Millisecond
)

type BlobClient struct {
	cluster    string
	volname    string
	master     util.MasterHelper
	conns      *pool.ConnPool
	partitions *PartitionCache
}

func NewBlobClient(volname, masters string) (*BlobClient, error) {
	client := new(BlobClient)
	client.volname = volname
	master := strings.Split(masters, ":")
	client.master = util.NewMasterHelper()
	for _, ip := range master {
		client.master.AddNode(ip)
	}
	client.conns = pool.NewConnPool()

	err := client.GetClusterInfo()
	if err != nil {
		errors.Annotatef(err, "NewBlobClient: get cluster info failed")
		return nil, err
	}

	client.partitions = NewPartitionCache()
	err = client.UpdateDataPartitions()
	if err != nil {
		errors.Annotatef(err, "NewBlobClient: update data partitions failed")
		return nil, err
	}
	go client.refresh()
	return client, nil
}

func (client *BlobClient) Write(data []byte) (key string, err error) {
	var (
		req, resp *proto.Packet
		dp        *DataPartition
	)

	retry := client.partitions.GetWritePartitionLen()
	for i := 0; i < retry; i++ {
		dp = client.partitions.GetWritePartition()
		if dp == nil {
			log.LogErrorf("Write: No write data partition")
			return "", syscall.ENOMEM
		}

		req = NewWritePacket(dp, data)
		resp, err = client.sendToDataPartition(dp, req)
		if err != nil || resp.ResultCode != proto.OpOk {
			log.LogErrorf("Write: dp(%v) err(%v) result(%v)", dp, err, resp.GetResultMesg())
			continue
		}

		partitionID, fileID, objID, size := ParsePacket(resp)
		if uint64(partitionID) != dp.PartitionID {
			log.LogWarnf("Write: req partition id(%v) resp partition id(%v)", dp.PartitionID, partitionID)
			continue
		}
		key = GenKey(client.cluster, client.volname, partitionID, fileID, objID, size)
		return key, nil
	}

	return "", syscall.EIO
}

func (client *BlobClient) Read(key string) (data []byte, err error) {
	cluster, volname, partitionID, fileID, objID, size, err := ParseKey(key)
	if err != nil || strings.Compare(cluster, client.cluster) != 0 || strings.Compare(volname, client.volname) != 0 {
		log.LogErrorf("Read: err(%v)", err)
		return nil, syscall.EINVAL
	}

	dp := client.partitions.Get(uint64(partitionID))
	if dp == nil {
		log.LogErrorf("Read: No partition, key(%v)", key)
	}

	req := NewReadPacket(partitionID, fileID, objID, size)
	resp, err := client.sendToDataPartition(dp, req)
	if err != nil || resp.ResultCode != proto.OpOk {
		log.LogErrorf("Read: key(%v) err(%v) result(%v)", key, err, resp.GetResultMesg())
		return nil, syscall.EIO
	}

	return resp.Data, nil
}

func (client *BlobClient) Delete(key string) (err error) {
	//TODO: Parse key
	//TODO: Create delete packet
	//TODO: Send request to data node
	//TODO: Read response
	return nil
}
