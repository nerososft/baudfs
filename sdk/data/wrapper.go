package data

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tiglabs/baudstorage/proto"
	"github.com/tiglabs/baudstorage/util"
	"github.com/tiglabs/baudstorage/util/log"
	"github.com/tiglabs/baudstorage/util/pool"
)

const (
	DataPartitionViewUrl        = "/client/dataPartitions"
	ActionGetDataPartitionView  = "ActionGetDataPartitionView"
	MinWritableDataPartitionNum = 10
)

var (
	MasterHelper = util.NewMasterHelper()
)

type DataPartition struct {
	PartitionID   uint32
	Status        uint8
	ReplicaNum    uint8
	PartitionType string
	Hosts         []string
}

type DataPartitionView struct {
	DataPartitions []*DataPartition
}

func (dp *DataPartition) String() string {
	return fmt.Sprintf("PartitionID(%v) Status(%v) ReplicaNum(%v) PartitionType(%v) Hosts(%v)",
		dp.PartitionID, dp.Status, dp.ReplicaNum, dp.PartitionType, dp.Hosts)
}

func (dp *DataPartition) GetAllAddrs() (m string) {
	return strings.Join(dp.Hosts[1:], proto.AddrSplit) + proto.AddrSplit
}

type Wrapper struct {
	sync.RWMutex
	volName     string
	masters     []string
	conns       *pool.ConnPool
	partitions  map[uint32]*DataPartition
	rwPartition []*DataPartition
}

func NewDataPartitionWrapper(volName, masterHosts string) (w *Wrapper, err error) {
	masters := strings.Split(masterHosts, ",")
	w = new(Wrapper)
	w.masters = masters
	for _, m := range w.masters {
		MasterHelper.AddNode(m)
	}
	w.volName = volName
	w.conns = pool.NewConnPool()
	w.rwPartition = make([]*DataPartition, 0)
	w.partitions = make(map[uint32]*DataPartition)
	if err = w.updateDataPartition(); err != nil {
		return
	}
	go w.update()
	return
}

func (w *Wrapper) update() {
	ticker := time.NewTicker(time.Minute)
	for {
		select {
		case <-ticker.C:
			w.updateDataPartition()
		}
	}
}

func (w *Wrapper) updateDataPartition() error {
	paras := make(map[string]string, 0)
	paras["name"] = w.volName
	msg, err := MasterHelper.Request(http.MethodGet, DataPartitionViewUrl, paras, nil)
	if err != nil {
		return err
	}

	view := &DataPartitionView{}
	if err = json.Unmarshal(msg, view); err != nil {
		return err
	}

	rwPartitionGroups := make([]*DataPartition, 0)
	for _, dp := range view.DataPartitions {
		if dp.Status == proto.ReadWrite {
			rwPartitionGroups = append(rwPartitionGroups, dp)
		}
	}
	if len(rwPartitionGroups) < MinWritableDataPartitionNum {
		err = fmt.Errorf("action[Wrapper.updateDataPartition] RW partitions[%v] Minimum[%v]", len(rwPartitionGroups), MinWritableDataPartitionNum)
		log.LogErrorf(err.Error())
		return err
	}

	w.rwPartition = rwPartitionGroups

	for _, dp := range view.DataPartitions {
		w.replaceOrInsertPartition(dp)
	}
	return nil
}

func (w *Wrapper) replaceOrInsertPartition(dp *DataPartition) {
	w.Lock()
	old, ok := w.partitions[dp.PartitionID]
	if ok {
		delete(w.partitions, dp.PartitionID)
	}
	w.partitions[dp.PartitionID] = dp
	w.Unlock()

	if ok && old.Status != dp.Status {
		log.LogInfof("DataPartition: status change (%v) -> (%v)", old, dp)
	}
}

func isExcluded(partitionId uint32, excludes []uint32) bool {
	for _, id := range excludes {
		if id == partitionId {
			return true
		}
	}
	return false
}

func (w *Wrapper) GetWriteDataPartition(exclude []uint32) (*DataPartition, error) {
	rwPartitionGroups := w.rwPartition
	if len(rwPartitionGroups) == 0 {
		return nil, fmt.Errorf("no writable data partition")
	}

	rand.Seed(time.Now().UnixNano())
	choose := rand.Intn(len(rwPartitionGroups))
	partition := rwPartitionGroups[choose]
	if !isExcluded(partition.PartitionID, exclude) {
		return partition, nil
	}

	for _, partition = range rwPartitionGroups {
		if !isExcluded(partition.PartitionID, exclude) {
			return partition, nil
		}
	}
	return nil, fmt.Errorf("no writable data partition")
}

func (w *Wrapper) GetDataPartition(partitionID uint32) (*DataPartition, error) {
	w.RLock()
	defer w.RUnlock()
	dp, ok := w.partitions[partitionID]
	if !ok {
		return nil, fmt.Errorf("DataPartition[%v] not exsit", partitionID)
	}
	return dp, nil
}

func (w *Wrapper) GetConnect(addr string) (*net.TCPConn, error) {
	return w.conns.Get(addr)
}

func (w *Wrapper) PutConnect(conn *net.TCPConn, forceClose bool) {
	w.conns.Put(conn, forceClose)
}
