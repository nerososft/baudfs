package master

import (
	"fmt"
	"github.com/tiglabs/baudstorage/proto"
	"github.com/tiglabs/baudstorage/util/log"
	"time"
)

func (partition *DataPartition) checkStatus(needLog bool, dpTimeOutSec int64) {
	partition.Lock()
	defer partition.Unlock()
	liveReplicas := partition.getLiveReplicasByPersistenceHosts(dpTimeOutSec)
	switch len(liveReplicas) {
	case (int)(partition.ReplicaNum):
		partition.Status = proto.ReadOnly
		if partition.checkReplicaStatusOnLiveNode(liveReplicas) == true {
			partition.Status = proto.ReadWrite
		}
	default:
		partition.Status = proto.ReadOnly
	}
	if needLog == true {
		msg := fmt.Sprintf("action[checkStatus],partitionID:%v  replicaNum:%v  liveReplicas:%v   Status:%v  RocksDBHost:%v ",
			partition.PartitionID, partition.ReplicaNum, len(liveReplicas), partition.Status, partition.PersistenceHosts)
		log.LogInfo(msg)
	}
}

func (partition *DataPartition) checkReplicaStatusOnLiveNode(liveReplicas []*DataReplica) (equal bool) {
	for _, replica := range liveReplicas {
		if replica.Status != proto.ReadWrite {
			return
		}
	}

	return true
}

func (partition *DataPartition) checkReplicaStatus(timeOutSec int64) {
	partition.Lock()
	defer partition.Unlock()
	for _, replica := range partition.Replicas {
		replica.IsLive(timeOutSec)
	}

}

func (partition *DataPartition) checkMiss(clusterID string, dataPartitionMissSec, dataPartitionWarnInterval int64) {
	partition.Lock()
	defer partition.Unlock()
	for _, replica := range partition.Replicas {
		if partition.isInPersistenceHosts(replica.Addr) && replica.CheckMiss(dataPartitionMissSec) == true && partition.needWarnMissDataPartition(replica.Addr, dataPartitionWarnInterval) {
			dataNode := replica.GetReplicaNode()
			var (
				lastReportTime time.Time
			)
			isActive := true
			if dataNode != nil {
				lastReportTime = dataNode.ReportTime
				isActive = dataNode.isActive
			}
			msg := fmt.Sprintf("action[checkMissErr],clusterID[%v] paritionID:%v  on Node:%v  "+
				"miss time > %v  lastRepostTime:%v   dnodeLastReportTime:%v  nodeisActive:%v So Migrate by manual",
				clusterID, partition.PartitionID, replica.Addr, dataPartitionMissSec, replica.ReportTime, lastReportTime, isActive)
			Warn(clusterID, msg)
		}
	}

	for _, addr := range partition.PersistenceHosts {
		if partition.missDataPartition(addr) == true && partition.needWarnMissDataPartition(addr, dataPartitionWarnInterval) {
			msg := fmt.Sprintf("action[checkMissErr], partitionID:%v  on Node:%v  "+
				"miss time  > :%v  but server not exsit So Migrate", partition.PartitionID, addr, dataPartitionMissSec)
			Warn(clusterID, msg)
		}
	}
}

func (partition *DataPartition) needWarnMissDataPartition(addr string, dataPartitionWarnInterval int64) (isWarn bool) {
	warnTime, ok := partition.MissNodes[addr]
	if !ok {
		partition.MissNodes[addr] = time.Now().Unix()
		isWarn = true
	} else {
		if time.Now().Unix()-warnTime > dataPartitionWarnInterval {
			isWarn = true
			partition.MissNodes[addr] = time.Now().Unix()
		}
	}

	return
}

func (partition *DataPartition) missDataPartition(addr string) (isMiss bool) {
	_, ok := partition.IsInReplicas(addr)

	if ok == false {
		isMiss = true
	}

	return
}

func (partition *DataPartition) checkDiskError(clusterID string) (diskErrorAddrs []string) {
	diskErrorAddrs = make([]string, 0)
	partition.Lock()
	defer partition.Unlock()
	for _, addr := range partition.PersistenceHosts {
		replica, ok := partition.IsInReplicas(addr)
		if !ok {
			continue
		}
		if replica.Status == proto.Unavaliable {
			diskErrorAddrs = append(diskErrorAddrs, addr)
		}
	}

	if len(diskErrorAddrs) != (int)(partition.ReplicaNum) && len(diskErrorAddrs) > 0 {
		partition.Status = proto.ReadOnly
	}

	for _, diskAddr := range diskErrorAddrs {
		msg := fmt.Sprintf("action[%v],clusterID[%v],partitionID:%v  On :%v  Disk Error,So Remove it From RocksDBHost",
			CheckDataPartitionDiskErrorErr, clusterID, partition.PartitionID, diskAddr)
		Warn(clusterID, msg)
	}

	return
}

func (partition *DataPartition) checkReplicationTask() (tasks []*proto.AdminTask) {
	var msg string
	tasks = make([]*proto.AdminTask, 0)
	if excessAddr, task, excessErr := partition.deleteExcessReplication(); excessErr != nil {
		tasks = append(tasks, task)
		msg = fmt.Sprintf("action[%v], partitionID:%v  Excess Replication"+
			" On :%v  Err:%v  rocksDBRecords:%v",
			DeleteExcessReplicationErr, partition.PartitionID, excessAddr, excessErr.Error(), partition.PersistenceHosts)
		log.LogWarn(msg)

	}
	if partition.Status == proto.ReadWrite {
		return
	}
	if lackTask, lackAddr, lackErr := partition.addLackReplication(); lackErr != nil {
		tasks = append(tasks, lackTask)
		msg = fmt.Sprintf("action[%v], partitionID:%v  Lack Replication"+
			" On :%v  Err:%v  PersistenceHosts:%v  new task to create DataReplica",
			AddLackReplicationErr, partition.PartitionID, lackAddr, lackErr.Error(), partition.PersistenceHosts)
		log.LogWarn(msg)
	} else {
		partition.setToNormal()
	}

	return
}

/*delete data replica excess replication ,range all data replicas
if data replica not in persistenceHosts then generator task to delete the replica*/
func (partition *DataPartition) deleteExcessReplication() (excessAddr string, task *proto.AdminTask, err error) {
	partition.Lock()
	defer partition.Unlock()
	for i := 0; i < len(partition.Replicas); i++ {
		replica := partition.Replicas[i]
		if ok := partition.isInPersistenceHosts(replica.Addr); !ok {
			excessAddr = replica.Addr
			log.LogError(fmt.Sprintf("action[deleteExcessReplication],partitionID:%v,has excess replication:%v",
				partition.PartitionID, excessAddr))
			err = DataReplicaExcessError
			task = partition.GenerateDeleteTask(excessAddr)
			break
		}
	}

	return
}

/*add data partition lack replication,range all RocksDBHost if Hosts not in Replicas,
then generator a task to OpRecoverCreateDataPartition to a new Node*/
func (partition *DataPartition) addLackReplication() (t *proto.AdminTask, lackAddr string, err error) {
	partition.Lock()
	defer partition.Unlock()
	for _, addr := range partition.PersistenceHosts {
		if _, ok := partition.IsInReplicas(addr); !ok {
			log.LogError(fmt.Sprintf("action[addLackReplication],partitionID:%v lack replication:%v",
				partition.PartitionID, addr))
			err = DataReplicaLackError
			lackAddr = addr
			t = partition.generateCreateTask(addr)
			partition.isRecover = true
			break
		}
	}

	return
}
